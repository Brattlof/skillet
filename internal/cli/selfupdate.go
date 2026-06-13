package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/Brattlof/skillet/internal/selfupdate"
)

func cmdSelfUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("self-update", flag.ContinueOnError)
	check := fs.Bool("check", false, "report whether an update is available without installing")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}

	if *check {
		tag, available, err := selfupdate.Check(ctx, Version)
		if err != nil {
			return err
		}
		if available {
			fmt.Printf("update available: %s (current %s) - run: skillet self-update\n", tag, Version)
		} else {
			fmt.Printf("skillet %s is up to date\n", Version)
		}
		return nil
	}

	tag, updated, err := selfupdate.Update(ctx, Version)
	if err != nil {
		return err
	}
	if !updated {
		fmt.Printf("skillet %s is already up to date\n", Version)
		return nil
	}
	fmt.Printf("updated skillet %s -> %s\n", Version, tag)
	return nil
}
