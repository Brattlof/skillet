// Command skillet is a small, zero-dependency package manager for AI-agent
// skills, slash commands, and hooks.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Brattlof/skillet/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:]))
}
