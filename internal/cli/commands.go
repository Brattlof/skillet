package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Brattlof/skillet/internal/install"
	"github.com/Brattlof/skillet/internal/registry"
)

const dirUsage = "target skills directory (default: ~/.claude/skills or $SKILLET_SKILLS_DIR)"

// parseArgs parses fs while tolerating flags placed after positional arguments.
// The stdlib flag package stops at the first non-flag token; this loops so that
// e.g. "add hello-skill --dir X" works the same as "add --dir X hello-skill".
// It returns the collected positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return errors.New("usage: skillet add <name> [--dir PATH]")
	}

	name := pos[0]
	entry, ok, err := registry.Find(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no skill named %q in the registry (try: skillet search %s)", name, name)
	}

	target, err := install.SkillsDir(*dir)
	if err != nil {
		return err
	}
	fmt.Printf("Installing %s from %s ...\n", entry.Name, entry.Repo)
	dest, err := install.Install(entry, target)
	if err != nil {
		return err
	}
	fmt.Printf("Installed %s -> %s\n", entry.Name, dest)
	return nil
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return errors.New("usage: skillet remove <name> [--dir PATH]")
	}

	target, err := install.SkillsDir(*dir)
	if err != nil {
		return err
	}
	name := pos[0]
	if err := install.Remove(name, target); err != nil {
		return err
	}
	fmt.Printf("Removed %s\n", name)
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}

	target, err := install.SkillsDir(*dir)
	if err != nil {
		return err
	}
	names, err := install.ListInstalled(target)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Printf("No skills installed in %s\n", target)
		return nil
	}
	fmt.Printf("Installed skills in %s:\n", target)
	for _, n := range names {
		fmt.Println("  -", n)
	}
	return nil
}

func cmdSearch(args []string) error {
	results, err := registry.Search(strings.Join(args, " "))
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Printf("No skills match %q\n", strings.Join(args, " "))
		return nil
	}
	printEntries(results)
	return nil
}

func cmdRegistry(args []string) error {
	entries, err := registry.Load()
	if err != nil {
		return err
	}
	printEntries(entries)
	return nil
}

func cmdPublish(args []string) error {
	fmt.Print(`Publish a skill to the registry:

  1. Fork github.com/Brattlof/skillet
  2. Add an entry to registry.json:

       {
         "name": "your-skill",
         "description": "One line on what it does.",
         "repo": "https://github.com/you/your-repo",
         "path": "path/to/skill-folder",
         "author": "you",
         "tags": ["example"]
       }

  3. Open a PR and tell us how you've used it (the one rule). See CONTRIBUTING.md.
`)
	return nil
}

func printEntries(entries []registry.Entry) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDESCRIPTION\tTAGS")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, truncate(e.Description, 60), strings.Join(e.Tags, ","))
	}
	_ = tw.Flush()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}
