// Package cli implements skillet's command dispatch.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Version is overridable at build time via -ldflags "-X ...cli.Version=...".
var Version = "0.1.0"

// Run dispatches a command and returns a process exit code.
func Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		usage(os.Stdout)
		return 0
	}

	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "add", "install":
		err = cmdAdd(ctx, rest)
	case "remove", "rm":
		err = cmdRemove(ctx, rest)
	case "list", "ls":
		err = cmdList(ctx, rest)
	case "search", "find":
		err = cmdSearch(ctx, rest)
	case "registry":
		err = cmdRegistry(ctx, rest)
	case "publish":
		err = cmdPublish(ctx, rest)
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
  add <name>       Install a skill from the registry
  remove <name>    Remove an installed skill
  list             List installed skills
  search <query>   Search the registry
  registry         Show every registry entry
  publish          How to publish your own skill
  version          Print the version
  help             Show this help

Flags:
  --dir PATH       Target skills dir (default: ~/.claude/skills or $SKILLET_SKILLS_DIR)

Environment:
  SKILLET_REGISTRY_URL   Override the registry index URL
  SKILLET_OFFLINE=1      Use only the cached or embedded index

Examples:
  skillet search research
  skillet add hello-skill
  skillet list
`)
}
