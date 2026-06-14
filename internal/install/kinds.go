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

// standardKinds are the file-based artifact kinds skillet installs into per-kind
// directories, in display order. (mcp is config-based and handled separately.)
var standardKinds = []string{"skill", "command", "hook", "agent", "output-style"}

// kindSubdir maps a kind to its directory under the agent config home (~/.claude).
func kindSubdir(kind string) string {
	switch kind {
	case "command":
		return "commands"
	case "hook":
		return "hooks"
	case "agent":
		return "agents"
	case "output-style":
		return "output-styles"
	default:
		return "skills"
	}
}

// DefaultTarget is the install target used when none is given.
const DefaultTarget = "claude"

// ValidTarget reports whether t is a known install target. The empty string means
// the default.
func ValidTarget(t string) bool {
	return t == "" || t == "claude" || t == "agents"
}

func unknownTarget(t string) error {
	return fmt.Errorf("unknown target %q (want claude or agents)", t)
}

// agentHome resolves the config home for a target: ~/.claude for Claude Code, or
// ~/.agents for the cross-tool Agent Skills standard directory that Cursor, Codex,
// Gemini CLI, and Copilot read.
func agentHome(target string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch target {
	case "", "claude":
		return filepath.Join(home, ".claude"), nil
	case "agents":
		return filepath.Join(home, ".agents"), nil
	default:
		return "", unknownTarget(target)
	}
}

// TargetDir resolves where an artifact of the given kind installs for a target. An
// explicit override (the --dir flag) wins for every kind and target. Under the
// default claude target, skills keep honoring the legacy SKILLET_SKILLS_DIR via
// SkillsDir. The agents target installs skills only, since slash commands and
// hooks are Claude Code specific.
func TargetDir(kind, target, override string) (string, error) {
	if override != "" {
		return expand(override)
	}
	switch target {
	case "", "claude":
		switch kind {
		case "", "skill":
			return SkillsDir("")
		case "command", "hook", "agent", "output-style":
			home, err := agentHome("claude")
			if err != nil {
				return "", err
			}
			return filepath.Join(home, kindSubdir(kind)), nil
		default:
			return "", fmt.Errorf("unknown kind %q", kind)
		}
	case "agents":
		switch kind {
		case "", "skill":
			home, err := agentHome("agents")
			if err != nil {
				return "", err
			}
			return filepath.Join(home, "skills"), nil
		case "command", "hook", "agent", "output-style":
			return "", fmt.Errorf("the agents target installs skills only; %s is specific to Claude Code", kind)
		default:
			return "", fmt.Errorf("unknown kind %q", kind)
		}
	default:
		return "", unknownTarget(target)
	}
}

// DirKind pairs an install directory with the kind it holds.
type DirKind struct {
	Kind string
	Dir  string
}

// ScanDirs returns the directories to inspect for installed artifacts for a
// target. With an override only that directory is scanned (kind unknown).
// Otherwise the per-kind directories for the target are returned: skills,
// commands, and hooks for claude; skills only for agents.
func ScanDirs(target, override string) ([]DirKind, error) {
	if override != "" {
		d, err := expand(override)
		if err != nil {
			return nil, err
		}
		return []DirKind{{Kind: "", Dir: d}}, nil
	}
	var kinds []string
	switch target {
	case "", "claude":
		kinds = standardKinds
	case "agents":
		kinds = []string{"skill"}
	default:
		return nil, unknownTarget(target)
	}
	out := make([]DirKind, 0, len(kinds))
	for _, k := range kinds {
		d, err := TargetDir(k, target, "")
		if err != nil {
			return nil, err
		}
		out = append(out, DirKind{Kind: k, Dir: d})
	}
	return out, nil
}

// FindInstall returns the directory a named artifact is installed in for a target,
// searching the scanned directories. It matches either an install dir or a
// manifest record.
func FindInstall(name, target, override string) (string, bool, error) {
	if !safeName(name) {
		return "", false, fmt.Errorf("invalid skill name %q", name)
	}
	dirs, err := ScanDirs(target, override)
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
