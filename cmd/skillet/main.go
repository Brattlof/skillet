// Command skillet is a small, zero-dependency package manager for AI-agent
// skills, slash commands, and hooks.
package main

import (
	"os"

	"github.com/Brattlof/skillet/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
