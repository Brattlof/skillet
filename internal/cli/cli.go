// Package cli implements skillet's command dispatch.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// Version is overridable at build time via -ldflags "-X ...cli.Version=...".
var Version = "0.1.0"

// errSilent makes Run exit with status 1 without printing an "error:" line, for
// commands like doctor that report their own findings.
var errSilent = errors.New("")

// Run dispatches a command and returns a process exit code.
func Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		usage(os.Stdout)
		return 0
	}

	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "add":
		err = cmdAdd(ctx, rest)
	case "install":
		err = cmdInstall(ctx, rest)
	case "lock":
		err = cmdLock(ctx, rest)
	case "update", "upgrade":
		err = cmdUpdate(ctx, rest)
	case "doctor":
		err = cmdDoctor(ctx, rest)
	case "remove", "rm":
		err = cmdRemove(ctx, rest)
	case "list", "ls":
		err = cmdList(ctx, rest)
	case "search", "find":
		err = cmdSearch(ctx, rest)
	case "info", "show":
		err = cmdInfo(ctx, rest)
	case "registry":
		err = cmdRegistry(ctx, rest)
	case "publish":
		err = cmdPublish(ctx, rest)
	case "completion":
		err = cmdCompletion(ctx, rest)
	case "self-update":
		err = cmdSelfUpdate(ctx, rest)
	case "__complete":
		err = cmdComplete(ctx, rest)
	case "version", "--version", "-v":
		fmt.Println("skillet", Version)
	case "help", "--help", "-h":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return 2
	}

	if err != nil {
		if errors.Is(err, errSilent) {
			return 1
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `skillet - a package manager for AI-agent skills

Usage:
  skillet <command> [args]

Commands:
  add <name>[@ref]   Install a skill, command, or hook (optionally pinned)
  install [--frozen] Restore from skillet.lock (--frozen only verifies)
  lock               Write skillet.lock from what is installed
  update [name]      Update an installed item, or all of them
  doctor             Check installed items for problems
  remove <name>      Remove an installed item
  list               List installed items
  search <query>     Search the registry (--kind, --tag to filter)
  info <name>        Show details of a registry entry
  registry           Show every registry entry
  publish            How to publish your own skill
  completion <sh>    Output a bash, zsh, or fish completion script
  self-update        Update the skillet binary to the latest release
  version            Print the version
  help               Show this help

Flags:
  --target NAME    claude (default, ~/.claude) or agents (~/.agents/skills,
                   read by Cursor, Codex, Gemini, and Copilot)
  --dir PATH       Exact install directory (overrides --target)

Environment:
  SKILLET_REGISTRY_URL   Override the registry index URL
  SKILLET_OFFLINE=1      Use only the cached or embedded index
  SKILLET_TARGET         Default target (claude or agents)
  SKILLET_SKILLS_DIR     Override the skills directory (skill kind only)
  SKILLET_CACHE_DIR      Override where the fetched index is cached
  SKILLET_LOCKFILE       Override the lockfile path (default: ./skillet.lock)

Examples:
  skillet add hello-skill
  skillet add hello-skill --target agents
  skillet install --frozen
`)
}
