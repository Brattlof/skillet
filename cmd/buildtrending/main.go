// Command buildtrending writes the trending and hot snapshot that the publish
// workflow serves next to the registry index. It reads the leaderboards from
// skills.sh, drops spam and failed audits, and fills in descriptions, reusing
// them from the previous snapshot (-previous) to keep requests low.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Brattlof/skillet/internal/skillssh"
	"github.com/Brattlof/skillet/internal/trending"
)

func main() {
	out := flag.String("out", "dist/trending.json", "output snapshot path")
	prev := flag.String("previous", "", "previous snapshot to reuse descriptions from (optional)")
	flag.Parse()
	if err := run(*out, *prev); err != nil {
		fmt.Fprintln(os.Stderr, "buildtrending:", err)
		os.Exit(1)
	}
}

func run(out, prev string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	client := skillssh.New()
	client.Pace = time.Second
	client.RetryRateLimited = true
	cfg := trending.Config{
		SkillsSH:    client,
		GitHubAPI:   os.Getenv("GITHUB_API_URL"),
		GitHubToken: os.Getenv("GITHUB_TOKEN"),
		Log:         os.Stderr,
	}
	if prev != "" {
		if b, err := os.ReadFile(prev); err == nil {
			if s, err := trending.Decode(b); err == nil {
				cfg.Previous = s
			} else {
				fmt.Fprintln(os.Stderr, "ignoring previous snapshot:", err)
			}
		}
	}

	snap, err := trending.Build(ctx, cfg)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d trending, %d hot)\n", out, len(snap.Trending), len(snap.Hot))
	return nil
}
