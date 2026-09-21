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
// The entry is assumed already validated by the registry (Repo is an https URL
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
	if prev, ok, rerr := ReadRecord(dir, e.Name); rerr == nil && ok {
		// The artifact name is read back from a stored record. Guard against a
		// tampered or corrupt record steering cleanup outside dir.
		art := prev.ArtifactName()
		old := filepath.Join(dir, art)
		safe := filepath.IsLocal(filepath.FromSlash(art))
		// Always clear the previous hook registration before reinstalling, even
		// when the artifact filename is unchanged. The entry's event or matcher may
		// have changed, and registerHook below keys blocks by matcher, so skipping
		// this would leave a stale block and the hook would also fire under the old
		// event or matcher.
		if safe && prev.Kind == "hook" && prev.Hook != nil {
			abs, aerr := filepath.Abs(old)
			if aerr != nil {
				abs = old
			}
			if uerr := unregisterHook(settingsPath(dir), prev.Hook.Event, prev.Hook.Matcher, abs); uerr != nil {
				return "", fmt.Errorf("clearing the previous hook registration for %s: %w", e.Name, uerr)
			}
		}
		// If a previous install used a different artifact (a hook whose script
		// extension changed, or a switch to another kind), remove it so the
		// reinstall does not orphan a file.
		if safe && old != dest {
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

	// Record the artifact's checksum in the current (v2) format for drift
	// detection by doctor and update.
	sum, err := hashArtifact(dest)
	if err != nil {
		os.RemoveAll(dest)
		return "", err
	}
	// Verify a pinned cksum in whatever format the registry published it in, so a
	// legacy v1 pin still validates against the artifact.
	if e.Cksum != "" {
		got, verr := hashArtifactAs(dest, cksumPrefix(e.Cksum))
		if verr != nil {
			os.RemoveAll(dest)
			return "", verr
		}
		if got != e.Cksum {
			os.RemoveAll(dest)
			return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", e.Name, got, e.Cksum)
		}
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
// begins with a dash as an option. Line-ending conversion is turned off in the
// clone's own config (so the pinned checkout inherits it too): Git for Windows
// defaults to CRLF, which would make the same artifact install and hash
// differently per OS, and breaks shell hooks.
func fetchRepo(ctx context.Context, e registry.Entry, tmp string) error {
	args := []string{"clone", "--config", "core.autocrlf=false", "--config", "core.eol=lf"}
	if e.Ref == "" {
		clone := exec.CommandContext(ctx, "git", append(args, "--depth", "1", "--", e.Repo, tmp)...)
		clone.Stderr = os.Stderr
		if err := clone.Run(); err != nil {
			return fmt.Errorf("cloning %s: %w", e.Repo, err)
		}
		return nil
	}
	clone := exec.CommandContext(ctx, "git", append(args, "--quiet", "--", e.Repo, tmp)...)
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

// VerifyChecksum reports whether the installed artifact for name in dir still
// matches want, recomputed in want's own format. A legacy (v1) checksum is
// verified with the v1 algorithm, so a record or lockfile written before the
// format change is not falsely reported as drift. ok is false if nothing is
// installed for name.
func VerifyChecksum(dir, name, want string) (match bool, ok bool, err error) {
	rec, hasRec, err := ReadRecord(dir, name)
	if err != nil {
		return false, false, err
	}
	artifact := name
	if hasRec {
		artifact = rec.ArtifactName()
	}
	dest := filepath.Join(dir, artifact)
	if _, serr := os.Stat(dest); serr != nil {
		return false, false, nil
	}
	got, err := hashArtifactAs(dest, cksumPrefix(want))
	if err != nil {
		return false, false, err
	}
	return got == want, true, nil
}

// hashArtifact hashes an installed artifact in the current (v2) format.
func hashArtifact(path string) (string, error) {
	return hashArtifactAs(path, cksumPrefixV2)
}

// HashArtifactAs computes the checksum of the artifact at path in a specific
// format: pass the legacy "sha256:" or the current "sha256.v2:" prefix. Normal
// installs always record the current format; this exists so tests and migration
// tooling can produce a checksum in either one.
func HashArtifactAs(path, prefix string) (string, error) {
	return hashArtifactAs(path, prefix)
}

// hashArtifactAs hashes an artifact in the format named by prefix, so a stored
// checksum can be re-verified in the same format it was written: a tree hash for a
// skill directory, a single-file hash otherwise.
func hashArtifactAs(path, prefix string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	switch {
	case prefix == cksumPrefixV1 && info.IsDir():
		return hashTreeV1(path)
	case prefix == cksumPrefixV1:
		return hashFileV1(path)
	case info.IsDir():
		return hashTree(path)
	default:
		return hashFile(path)
	}
}

// Checksum format versions, encoded as the cksum's prefix. v1 folded permission
// bits into the hash, and those vary with the umask on Unix and are never
// executable on Windows. It also sorted paths by the OS separator and framed files
// ambiguously, so bytes moved from the end of one file into a new file could keep
// the hash. v2 hashes only contents and slash paths, so an artifact hashes the same
// on every platform. New checksums are written as v2; v1 checksums are still
// verified in their own format, so records and lockfiles written before the change
// are not falsely reported as drift.
const (
	cksumPrefixV1 = "sha256:"
	cksumPrefixV2 = "sha256.v2:"
)

// cksumPrefix returns the format prefix carried by a stored checksum, so it can
// be recomputed and compared in the same format it was written. An empty or
// unrecognized value defaults to the current v2 format.
func cksumPrefix(c string) string {
	if strings.HasPrefix(c, cksumPrefixV2) {
		return cksumPrefixV2
	}
	if strings.HasPrefix(c, cksumPrefixV1) {
		return cksumPrefixV1
	}
	return cksumPrefixV2
}

// hashFile returns the v2 checksum of a single file: a sha256 over its contents.
func hashFile(path string) (string, error) {
	sum, err := sha256File(path)
	if err != nil {
		return "", err
	}
	return cksumPrefixV2 + hex.EncodeToString(sum), nil
}

// hashTree returns the v2 checksum of the file tree rooted at root. Files are
// hashed in sorted slash-path order, each as its relative slash path, a NUL, the
// hex sha256 of its contents, and a newline. A path cannot contain NUL and the
// digest has a fixed length, so no two trees share an encoding, and sorting the
// slash form keeps the order the same on every OS.
func hashTree(root string) (string, error) {
	var rels []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(rels)

	h := sha256.New()
	for _, rel := range rels {
		sum, err := sha256File(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%x\n", rel, sum)
	}
	return cksumPrefixV2 + hex.EncodeToString(h.Sum(nil)), nil
}

// sha256File returns the sha256 of a file's contents.
func sha256File(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// hashFileV1 and hashTreeV1 compute the legacy v1 checksum, kept only to verify
// records and pins written before v2. Do not change them: a v1 checksum has to
// recompute exactly as it did when it was written.
func hashFileV1(path string) (string, error) {
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
	return cksumPrefixV1 + hex.EncodeToString(h.Sum(nil)), nil
}

func hashTreeV1(root string) (string, error) {
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
	return cksumPrefixV1 + hex.EncodeToString(h.Sum(nil)), nil
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
func copyFile(src, dst string) (err error) {
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
	defer func() {
		// Close can be where a buffered write finally fails, so surface that error
		// when the copy otherwise succeeded; ignoring it could install a truncated
		// file that then passes as complete.
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if info, err := os.Stat(src); err == nil {
		_ = os.Chmod(dst, info.Mode())
	}
	return nil
}
