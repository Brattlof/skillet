package install

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Brattlof/skillet/internal/skillssh"
)

// Bounds for searching a cloned repo for skill folders, so a huge or hostile
// repo cannot make discovery run unbounded.
const (
	maxDiscoverDepth   = 8      // folder depth below the repo root
	maxDiscoverEntries = 200000 // files and folders visited
	maxSkillFiles      = 5000   // SKILL.md files read
)

// skipDirs are never searched; they hold build output or dependencies, not
// skills. This mirrors the skills CLI that skills.sh indexes with.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "dist": true, "build": true, "__pycache__": true}

var errRepoTooLarge = errors.New("repository too large to search for skills")

// findSkill returns the slash path, relative to root, of the folder holding the
// skill with the given slug, or "." for a skill at the repo root.
func findSkill(root, slug string) (string, error) {
	cands, err := skillCandidates(root)
	if err != nil {
		return "", err
	}
	c, ok := skillssh.Match(slug, cands)
	if !ok {
		return "", fmt.Errorf("no skill named %q in the repository (found %d skill(s))", slug, len(cands))
	}
	return c.Path, nil
}

// skillCandidates lists every folder under root with a SKILL.md, with the name
// from its header. Symlinks are never followed: WalkDir does not descend into a
// symlinked folder, and a SKILL.md that is a symlink is skipped, so a repo
// cannot steer discovery at a file outside the clone.
func skillCandidates(root string) ([]skillssh.Candidate, error) {
	var cands []skillssh.Candidate
	visited := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if visited++; visited > maxDiscoverEntries {
			return errRepoTooLarge
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel != "." && (skipDirs[d.Name()] || strings.Count(filepath.ToSlash(rel), "/") >= maxDiscoverDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(d.Name(), "SKILL.md") || !d.Type().IsRegular() {
			return nil
		}
		// The path is printed and recorded; a folder name with terminal control
		// characters is a hostile repo, not a skill.
		if !skillssh.Printable(rel) {
			return nil
		}
		if len(cands) >= maxSkillFiles {
			return errRepoTooLarge
		}
		fm, err := readFrontmatter(p)
		if err != nil {
			return err
		}
		cands = append(cands, skillssh.Candidate{Path: filepath.ToSlash(filepath.Dir(rel)), Name: fm.Name})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cands, nil
}

// readFrontmatter parses the header of the SKILL.md at p, reading at most the
// bytes the parser looks at.
func readFrontmatter(p string) (skillssh.Frontmatter, error) {
	f, err := os.Open(p)
	if err != nil {
		return skillssh.Frontmatter{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return skillssh.Frontmatter{}, err
	}
	return skillssh.ParseFrontmatter(string(b)), nil
}
