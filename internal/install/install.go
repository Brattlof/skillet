// Package install fetches skills and manages the local skills directory.
package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Brattlof/skillet/internal/registry"
)

// SkillsDir resolves the directory skills are installed into.
// Priority: explicit override > $SKILLET_SKILLS_DIR > ~/.claude/skills
func SkillsDir(override string) (string, error) {
	if override != "" {
		return expand(override)
	}
	if env := os.Getenv("SKILLET_SKILLS_DIR"); env != "" {
		return expand(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

func expand(p string) (string, error) {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

// Install fetches the entry's repo (pinned to e.Ref when set), installs it into
// dir, verifies e.Cksum when set, and returns the install path. A skill is copied
// as a directory tree into dir/<name>; a command, hook, agent, or output-style is
// a single file copied to dir/<name><ext>, and a hook is also registered in the
// adjacent settings.json.
// The entry is assumed already validated by the registry (Repo is an http(s) URL
// and Ref is a plain git ref), which keeps the git calls below safe.
func Install(ctx context.Context, e registry.Entry, dir string) (string, error) {
	// Defense in depth: never let a name or path escape the install directory,
	// even if a caller forgot to validate (for example restoring from a lockfile).
	if !safeName(e.Name) {
		return "", fmt.Errorf("unsafe skill name %q", e.Name)
	}
	if !filepath.IsLocal(filepath.FromSlash(e.Path)) {
		return "", fmt.Errorf("unsafe skill path %q", e.Path)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git is required to install skills: %w", err)
	}
	kind := e.KindOrDefault()
	if kind == "hook" && e.Hook == nil {
		return "", fmt.Errorf("hook %s has no registration spec", e.Name)
	}

	tmp, err := os.MkdirTemp("", "skillet-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	if err := fetchRepo(ctx, e, tmp); err != nil {
		return "", err
	}

	src := filepath.Join(tmp, filepath.FromSlash(e.Path))
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("path %q not found in %s", e.Path, e.Repo)
	}
	// IsLocal checks the path only lexically and cannot see symlinks. A repo
	// could commit a symlink, or a symlinked directory along the path, that
	// points outside the clone; os.Stat and copyFile would then follow it and
	// copy an unrelated host file into the artifact. Resolve the path and
	// require it to stay inside the clone.
	realRoot, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return "", err
	}
	realSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path %q in %s: %w", e.Path, e.Repo, err)
	}
	if rel, rerr := filepath.Rel(realRoot, realSrc); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q resolves outside the repo (symlink escape)", e.Path)
	}

	artifact := artifactName(kind, e.Name, src)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, artifact)
	// If a previous install of this name used a different artifact (a hook whose
	// script extension changed, or a switch to another kind), remove it first so
	// the reinstall does not orphan a file or a stale settings.json registration.
	if prev, ok, rerr := ReadRecord(dir, e.Name); rerr == nil && ok {
		// The artifact name is read back from a stored record. Guard against a
		// tampered or corrupt record steering the cleanup RemoveAll outside dir.
		art := prev.ArtifactName()
		if old := filepath.Join(dir, art); old != dest && filepath.IsLocal(filepath.FromSlash(art)) {
			if prev.Kind == "hook" && prev.Hook != nil {
				abs, aerr := filepath.Abs(old)
				if aerr != nil {
					abs = old
				}
				if uerr := unregisterHook(settingsPath(dir), prev.Hook.Event, prev.Hook.Matcher, abs); uerr != nil {
					return "", fmt.Errorf("clearing the previous hook registration for %s: %w", e.Name, uerr)
				}
			}
			if rerr := os.RemoveAll(old); rerr != nil {
				return "", rerr
			}
		}
	}
	if err := os.RemoveAll(dest); err != nil {
		return "", err
	}

	switch kind {
	case "skill":
		if !info.IsDir() {
			return "", fmt.Errorf("skill path %q must be a directory in %s", e.Path, e.Repo)
		}
		if err := copyDir(src, dest); err != nil {
			return "", err
		}
	case "command", "hook", "agent", "output-style":
		if info.IsDir() {
			return "", fmt.Errorf("%s path %q must be a single file in %s", kind, e.Path, e.Repo)
		}
		if err := copyFile(src, dest); err != nil {
			return "", err
		}
		if kind == "hook" {
			if err := os.Chmod(dest, 0o755); err != nil {
				os.Remove(dest)
				return "", err
			}
		}
	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}

	// Always hash the installed artifact: it verifies a pinned cksum and is
	// recorded for drift detection by doctor and update.
	sum, err := hashArtifact(dest)
	if err != nil {
		os.RemoveAll(dest)
		return "", err
	}
	if e.Cksum != "" && sum != e.Cksum {
		os.RemoveAll(dest)
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", e.Name, sum, e.Cksum)
	}

	commit, _ := resolveCommit(ctx, tmp) // best-effort; empty if git cannot report it
	rec := Record{
		Name:        e.Name,
		Repo:        e.Repo,
		Path:        e.Path,
		Kind:        kind,
		Artifact:    artifact,
		Ref:         e.Ref,
		Commit:      commit,
		Cksum:       sum,
		Hook:        e.Hook,
		InstalledAt: time.Now(),
	}
	if err := writeRecord(dir, rec); err != nil {
		os.RemoveAll(dest)
		return "", fmt.Errorf("recording install of %s: %w", e.Name, err)
	}

	if kind == "hook" {
		abs, err := filepath.Abs(dest)
		if err != nil {
			abs = dest
		}
		if err := registerHook(settingsPath(dir), e.Hook.Event, e.Hook.Matcher, abs); err != nil {
			os.RemoveAll(dest)
			_ = removeRecord(dir, e.Name)
			return "", fmt.Errorf("registering hook %s: %w", e.Name, err)
		}
	}
	return dest, nil
}

// artifactName is the installed basename for an entry. A skill keeps its name as a
// directory; a command, agent, or output style is a .md file (the shape Claude Code
// reads); a hook keeps the source script's extension.
func artifactName(kind, name, src string) string {
	switch kind {
	case "command", "agent", "output-style":
		return name + ".md"
	case "hook":
		return name + filepath.Ext(src)
	default:
		return name
	}
}

// resolveCommit returns the commit currently checked out in repoDir.
func resolveCommit(ctx context.Context, repoDir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Update reinstalls e into dir and returns the previous and current provenance
// records, so the caller can report what changed. The previous record is zero if
// the skill was not installed before.
func Update(ctx context.Context, e registry.Entry, dir string) (prev Record, cur Record, err error) {
	prev, _, _ = ReadRecord(dir, e.Name)
	if _, ierr := Install(ctx, e, dir); ierr != nil {
		return Record{}, Record{}, ierr
	}
	cur, _, err = ReadRecord(dir, e.Name)
	return prev, cur, err
}

// fetchRepo clones e.Repo into tmp. With no ref it shallow-clones the default
// branch; with a ref (any commit or tag) it does a full clone and checks it out.
// The "--" and "--end-of-options" separators stop git from parsing a value that
// begins with a dash as an option.
func fetchRepo(ctx context.Context, e registry.Entry, tmp string) error {
	if e.Ref == "" {
		clone := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--", e.Repo, tmp)
		clone.Stderr = os.Stderr
		if err := clone.Run(); err != nil {
			return fmt.Errorf("cloning %s: %w", e.Repo, err)
		}
		return nil
	}
	clone := exec.CommandContext(ctx, "git", "clone", "--quiet", "--", e.Repo, tmp)
	clone.Stderr = os.Stderr
	if err := clone.Run(); err != nil {
		return fmt.Errorf("cloning %s: %w", e.Repo, err)
	}
	checkout := exec.CommandContext(ctx, "git", "-C", tmp, "checkout", "--quiet", "--end-of-options", e.Ref)
	checkout.Stderr = os.Stderr
	if err := checkout.Run(); err != nil {
		return fmt.Errorf("checking out %s@%s: %w", e.Repo, e.Ref, err)
	}
	return nil
}

// Remove deletes an installed artifact and its provenance record. A hook is
// un-registered from settings.json before its script is deleted, so the settings
// never point at a missing file.
func Remove(name, dir string) error {
	rec, hasRec, err := ReadRecord(dir, name)
	if err != nil {
		return err
	}
	artifact := name
	if hasRec {
		artifact = rec.ArtifactName()
	}
	dest := filepath.Join(dir, artifact)
	if _, serr := os.Stat(dest); os.IsNotExist(serr) && !hasRec {
		return fmt.Errorf("%q is not installed in %s", name, dir)
	}

	if hasRec && rec.Kind == "hook" && rec.Hook != nil {
		abs, aerr := filepath.Abs(dest)
		if aerr != nil {
			abs = dest
		}
		if uerr := unregisterHook(settingsPath(dir), rec.Hook.Event, rec.Hook.Matcher, abs); uerr != nil {
			return fmt.Errorf("unregistering hook %s: %w", name, uerr)
		}
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return removeRecord(dir, name)
}

// ListInstalled returns the installed artifact names in dir for the given kind:
// directories for a skill, files for the single-file kinds. An empty kind (a --dir
// override, where the kind is unknown) lists every visible entry. The .skillet
// metadata directory and other hidden entries are always skipped.
func ListInstalled(dir, kind string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		switch kind {
		case "skill":
			if e.IsDir() {
				names = append(names, e.Name())
			}
		case "command", "hook", "agent", "output-style":
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		default:
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// CurrentChecksum returns the on-disk checksum of an installed artifact, resolving
// its path from the manifest record. ok is false if nothing is installed for name.
func CurrentChecksum(dir, name string) (sum string, ok bool, err error) {
	rec, hasRec, err := ReadRecord(dir, name)
	if err != nil {
		return "", false, err
	}
	artifact := name
	if hasRec {
		artifact = rec.ArtifactName()
	}
	dest := filepath.Join(dir, artifact)
	if _, serr := os.Stat(dest); serr != nil {
		return "", false, nil
	}
	sum, err = hashArtifact(dest)
	if err != nil {
		return "", false, err
	}
	return sum, true, nil
}

// hashArtifact hashes an installed artifact: a tree hash for a skill directory, a
// single-file hash otherwise.
func hashArtifact(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return hashTree(path)
	}
	return hashFile(path)
}

// hashFile returns a sha256 over a single file's permission bits and contents, in
// the same "sha256:" form as hashTree. The mode is included so a lost executable
// bit (which matters for a hook script) changes the hash.
func hashFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	fmt.Fprintf(h, "%o\n", info.Mode().Perm())
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// hashTree returns a deterministic sha256 over the file tree rooted at root.
// Files are hashed in sorted path order as: relative slash-path, a space, the
// octal permission bits, a newline, then the streamed file contents. The mode is
// included so a lost or added executable bit changes the hash.
func hashTree(root string) (string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)

	h := sha256.New()
	for _, p := range files {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s %o\n", filepath.ToSlash(rel), info.Mode().Perm())
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir // never copy version-control metadata
			}
			return nil // a gitlink file for a submodule
		}
		// Skip symlinks rather than follow them: a repo could point one at a
		// file outside the clone and have its contents copied into the skill.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// copyFile copies the file at src to dst, following src if it is a symlink. Its
// callers (Install after the containment check, and copyDir which skips symlink
// entries) guarantee src cannot point outside the cloned repo.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if info, err := os.Stat(src); err == nil {
		_ = os.Chmod(dst, info.Mode())
	}
	return nil
}
