// Command buildindex compiles the per-kind shards (skills, commands, hooks) into
// a single index.json. CI runs it with -check on pull requests and writes the
// index on merge to main.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Brattlof/skillet/internal/registry"
)

func main() {
	check := flag.Bool("check", false, "validate the shards without writing the index")
	root := flag.String("root", ".", "repository root holding the skills, commands, and hooks shard directories")
	out := flag.String("out", "dist/index.json", "output index path")
	flag.Parse()

	entries, err := registry.BuildIndex(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "buildindex:", err)
		os.Exit(1)
	}
	if *check {
		fmt.Printf("ok: %d skill(s) validated\n", len(entries))
		return
	}

	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "buildindex:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "buildindex:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "buildindex:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d skill(s))\n", *out, len(entries))
}
