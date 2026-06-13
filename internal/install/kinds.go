package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safeName reports whether name is a single safe path component (no separators,
// no traversal), so it can never escape the install directory.
func safeName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) && filepath.IsLocal(name)
}

// standardKinds are the artifact kinds skillet installs, in display order.
var standardKinds = []string{"skill", "command", "hook"}

// kindSubdir maps a kind to its directory under the agent config home (~/.claude).
func kindSubdir(kind string) string {
	switch kind {
	case "command":
		return "commands"
	case "hook":
		return "hooks"
	default:
		return "skills"
	}
}

// TargetDir resolves where an artifact of the given kind installs. An explicit
// override (the --dir flag) wins for every kind. Skills keep honoring the legacy
// SKILLET_SKILLS_DIR via SkillsDir.
func TargetDir(kind, override string) (string, error) {
	if override != "" {
		return expand(override)
	}
	switch kind {
	case "", "skill":
		return SkillsDir("")
	case "command", "hook":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude", kindSubdir(kind)), nil
	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}
}

// DirKind pairs an install directory with the kind it holds.
type DirKind struct {
	Kind string
	Dir  string
}

// ScanDirs returns the directories to inspect for installed artifacts. With an
// override only that directory is scanned (kind unknown); otherwise the standard
// per-kind directories are returned.
func ScanDirs(override string) ([]DirKind, error) {
	if override != "" {
		d, err := expand(override)
		if err != nil {
			return nil, err
		}
		return []DirKind{{Kind: "", Dir: d}}, nil
	}
	out := make([]DirKind, 0, len(standardKinds))
	for _, k := range standardKinds {
		d, err := TargetDir(k, "")
		if err != nil {
			return nil, err
		}
		out = append(out, DirKind{Kind: k, Dir: d})
	}
	return out, nil
}

// FindInstall returns the directory a named artifact is installed in, searching
// the scanned directories. It matches either an install dir or a manifest record.
func FindInstall(name, override string) (string, bool, error) {
	if !safeName(name) {
		return "", false, fmt.Errorf("invalid skill name %q", name)
	}
	dirs, err := ScanDirs(override)
	if err != nil {
		return "", false, err
	}
	for _, dk := range dirs {
		if info, err := os.Stat(filepath.Join(dk.Dir, name)); err == nil && info.IsDir() {
			return dk.Dir, true, nil
		}
		if _, ok, _ := ReadRecord(dk.Dir, name); ok {
			return dk.Dir, true, nil
		}
	}
	return "", false, nil
}
