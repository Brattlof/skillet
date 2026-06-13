// Package install fetches skills and manages the local skills directory.
package install

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

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

// Install shallow-clones the entry's repo and copies its skill folder into
// dir/<name>, replacing any existing install. It returns the install path.
func Install(e registry.Entry, dir string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git is required to install skills: %w", err)
	}

	tmp, err := os.MkdirTemp("", "skillet-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	clone := exec.Command("git", "clone", "--depth", "1", e.Repo, tmp)
	clone.Stderr = os.Stderr
	if err := clone.Run(); err != nil {
		return "", fmt.Errorf("cloning %s: %w", e.Repo, err)
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
	return dest, nil
}

// Remove deletes an installed skill.
func Remove(name, dir string) error {
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return fmt.Errorf("%q is not installed in %s", name, dir)
	}
	return os.RemoveAll(dest)
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
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
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
