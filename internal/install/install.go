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

// Install fetches the entry's repo (pinned to e.Ref when set), copies its skill
// folder into dir/<name>, verifies e.Cksum when set, and returns the install path.
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
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("skill path %q not found in %s", e.Path, e.Repo)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, e.Name)
	if err := os.RemoveAll(dest); err != nil {
		return "", err
	}
	if err := copyDir(src, dest); err != nil {
		return "", err
	}

	// Always hash the installed tree: it verifies a pinned cksum and is recorded
	// for drift detection by doctor and update.
	sum, err := hashTree(dest)
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
		Kind:        e.KindOrDefault(),
		Ref:         e.Ref,
		Commit:      commit,
		Cksum:       sum,
		InstalledAt: time.Now(),
	}
	if err := writeRecord(dir, rec); err != nil {
		os.RemoveAll(dest)
		return "", fmt.Errorf("recording install of %s: %w", e.Name, err)
	}
	return dest, nil
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

// Remove deletes an installed skill.
func Remove(name, dir string) error {
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return fmt.Errorf("%q is not installed in %s", name, dir)
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return removeRecord(dir, name)
}

// ListInstalled returns the names of installed skills (top-level directories).
func ListInstalled(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		// Skip the .skillet metadata dir and any other hidden entries.
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
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
	gitDir := string(os.PathSeparator) + ".git" + string(os.PathSeparator)
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(path, gitDir) {
			return nil // never copy version-control metadata
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
