package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
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

func cmdAdd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return errors.New("usage: skillet add <name>[@ref] [--dir PATH]")
	}

	name, ref := splitNameRef(pos[0])
	entry, ok, err := registry.Find(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no skill named %q in the registry (try: skillet search %s)", name, name)
	}
	if ref != "" {
		entry.Ref = ref
		if err := registry.Validate(entry); err != nil {
			return err
		}
	}
	if entry.Ref == "" && entry.Cksum == "" {
		fmt.Fprintf(os.Stderr,
			"warning: %s is not pinned (no ref or checksum); its content can change after review - pin it with 'skillet add %s@<ref>'\n",
			entry.Name, entry.Name)
	}

	target, err := install.TargetDir(entry.KindOrDefault(), *dir)
	if err != nil {
		return err
	}
	fmt.Printf("Installing %s from %s ...\n", entry.Name, entry.Repo)
	dest, err := install.Install(ctx, entry, target)
	if err != nil {
		return err
	}
	fmt.Printf("Installed %s -> %s\n", entry.Name, dest)

	if rec, recorded, _ := install.ReadRecord(target, entry.Name); recorded {
		if err := upsertLock(rec); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not update", lockPath()+":", err)
		}
	}
	return nil
}

func cmdUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}

	type item struct{ name, dir string }
	var items []item
	if len(pos) >= 1 {
		name := pos[0]
		target, found, err := install.FindInstall(name, *dir)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%q is not installed (use: skillet add %s)", name, name)
		}
		items = []item{{name, target}}
	} else {
		dirs, err := install.ScanDirs(*dir)
		if err != nil {
			return err
		}
		for _, dk := range dirs {
			recs, err := install.Records(dk.Dir)
			if err != nil {
				return err
			}
			for _, r := range recs {
				items = append(items, item{r.Name, dk.Dir})
			}
		}
		if len(items) == 0 {
			fmt.Println("No skills installed")
			return nil
		}
	}

	var updated, unchanged, skipped int
	for _, it := range items {
		entry, ok, err := registry.Find(ctx, it.name)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Printf("skipped %s (not in the registry)\n", it.name)
			skipped++
			continue
		}
		prev, cur, err := install.Update(ctx, entry, it.dir)
		if err != nil {
			return fmt.Errorf("updating %s: %w", it.name, err)
		}
		switch {
		case prev.Commit == "":
			fmt.Printf("installed %s (%s)\n", it.name, short(cur.Commit))
			updated++
		case prev.Commit != cur.Commit:
			fmt.Printf("updated %s %s -> %s\n", it.name, short(prev.Commit), short(cur.Commit))
			updated++
		default:
			fmt.Printf("%s already up to date (%s)\n", it.name, short(cur.Commit))
			unchanged++
		}
	}
	fmt.Printf("\n%d updated, %d unchanged, %d skipped\n", updated, unchanged, skipped)
	return nil
}

func cmdDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}

	dirs, err := install.ScanDirs(*dir)
	if err != nil {
		return err
	}

	// Registry membership is a warning, not a hard failure, and is best-effort:
	// if the index cannot be loaded we simply skip that check.
	inRegistry := map[string]bool{}
	registryKnown := false
	if entries, lerr := registry.Load(ctx); lerr == nil {
		registryKnown = true
		for _, e := range entries {
			inRegistry[strings.ToLower(e.Name)] = true
		}
	}

	var ok, warnings, problems, total int
	for _, dk := range dirs {
		diags, err := install.Diagnose(dk.Dir, dk.Kind)
		if err != nil {
			return err
		}
		for _, d := range diags {
			total++
			switch d.Status {
			case install.StatusOK:
				if registryKnown && !inRegistry[strings.ToLower(d.Name)] {
					fmt.Printf("warn  %s: not in the registry\n", d.Name)
					warnings++
				} else {
					fmt.Printf("ok    %s\n", d.Name)
					ok++
				}
			case install.StatusNoRecord:
				fmt.Printf("warn  %s: %s\n", d.Name, d.Status)
				warnings++
			default:
				fmt.Printf("FAIL  %s: %s\n", d.Name, d.Status)
				problems++
			}
		}
	}
	if total == 0 {
		fmt.Println("No skills installed")
		return nil
	}

	fmt.Printf("\n%d ok, %d warning(s), %d problem(s)\n", ok, warnings, problems)
	if problems > 0 {
		return errSilent
	}
	return nil
}

func short(commit string) string {
	if commit == "" {
		return "unknown"
	}
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func cmdRemove(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return errors.New("usage: skillet remove <name> [--dir PATH]")
	}

	name := pos[0]
	target, found, err := install.FindInstall(name, *dir)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%q is not installed", name)
	}
	if err := install.Remove(name, target); err != nil {
		return err
	}
	fmt.Printf("Removed %s\n", name)
	return nil
}

func cmdList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}

	dirs, err := install.ScanDirs(*dir)
	if err != nil {
		return err
	}

	// Best-effort registry lookup (offline uses the cache/embedded index).
	entries := map[string]registry.Entry{}
	if loaded, lerr := registry.Load(ctx); lerr == nil {
		for _, e := range loaded {
			entries[strings.ToLower(e.Name)] = e
		}
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	rows := 0
	for _, dk := range dirs {
		recs, err := install.Records(dk.Dir)
		if err != nil {
			return err
		}
		installed, err := install.ListInstalled(dk.Dir, dk.Kind)
		if err != nil {
			return err
		}

		display := map[string]string{}
		recByName := map[string]install.Record{}
		recArtifacts := map[string]bool{}
		for _, r := range recs {
			k := strings.ToLower(r.Name)
			display[k] = r.Name
			recByName[k] = r
			recArtifacts[r.ArtifactName()] = true
		}
		for _, n := range installed {
			if recArtifacts[n] {
				continue // a command/hook file already shown under its record's name
			}
			display[strings.ToLower(n)] = n
		}
		if len(display) == 0 {
			continue
		}

		keys := make([]string, 0, len(display))
		for k := range display {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		if rows == 0 {
			fmt.Fprintln(tw, "NAME\tKIND\tINSTALLED\tSOURCE\tSTATUS")
		}
		for _, k := range keys {
			rec, hasRec := recByName[k]
			entry, inReg := entries[k]

			kind := dk.Kind
			if hasRec && rec.Kind != "" {
				kind = rec.Kind
			}
			if kind == "" {
				kind = "?"
			}
			installedCol, source := "?", "?"
			if hasRec {
				switch {
				case rec.Commit != "":
					installedCol = short(rec.Commit)
				case rec.Ref != "":
					installedCol = rec.Ref
				}
				source = stripScheme(rec.Repo)
			} else if inReg {
				source = stripScheme(entry.Repo)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", display[k], kind, installedCol, source, listStatus(hasRec, inReg, rec, entry))
			rows++
		}
	}
	if rows == 0 {
		fmt.Println("No skills installed")
		return nil
	}
	return tw.Flush()
}

// listStatus reports how an installed skill compares to the registry. It uses the
// requested ref recorded at install time (not the resolved commit) so a pinned
// tag is compared like-for-like. It cannot detect upstream drift for an unpinned
// entry without a network fetch, so those are reported as "tracking".
func listStatus(hasRec, inReg bool, rec install.Record, e registry.Entry) string {
	switch {
	case !hasRec:
		return "no record"
	case !inReg:
		return "not in registry"
	case e.Ref != "" && e.Ref != rec.Ref:
		return "update available" // registry moved its pin
	case e.Cksum != "" && e.Cksum != rec.Cksum:
		return "update available" // registry repinned the content
	case e.Ref != "" && e.Ref == rec.Ref:
		return "up to date" // registry pin matches the install
	case rec.Ref != "":
		return "pinned" // user-pinned install; registry is unpinned, cannot compare offline
	case e.Ref == "" && e.Cksum == "":
		return "tracking"
	default:
		return "up to date"
	}
}

func stripScheme(repo string) string {
	repo = strings.TrimPrefix(repo, "https://")
	repo = strings.TrimPrefix(repo, "http://")
	if repo == "" {
		return "?"
	}
	return repo
}

func cmdSearch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	kind := fs.String("kind", "", "filter by kind (skill, command, hook)")
	tag := fs.String("tag", "", "filter by tag")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}

	query := strings.Join(pos, " ")
	results, err := registry.Search(ctx, query)
	if err != nil {
		return err
	}
	if *kind != "" {
		var filtered []registry.Entry
		for _, e := range results {
			if e.KindOrDefault() == *kind {
				filtered = append(filtered, e)
			}
		}
		results = filtered
	}
	if *tag != "" {
		var filtered []registry.Entry
		for _, e := range results {
			for _, t := range e.Tags {
				if strings.EqualFold(t, *tag) {
					filtered = append(filtered, e)
					break
				}
			}
		}
		results = filtered
	}
	if len(results) == 0 {
		fmt.Printf("No skills match %q\n", query)
		return nil
	}
	printEntries(results)
	return nil
}

func cmdRegistry(ctx context.Context, _ []string) error {
	entries, err := registry.Load(ctx)
	if err != nil {
		return err
	}
	printEntries(entries)
	return nil
}

func cmdInfo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return errors.New("usage: skillet info <name> [--dir PATH]")
	}
	name := pos[0]

	entry, ok, err := registry.Find(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no skill named %q in the registry (try: skillet search %s)", name, name)
	}

	fmt.Println(entry.Name)
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "  Description\t%s\n", entry.Description)
	fmt.Fprintf(tw, "  Kind\t%s\n", entry.KindOrDefault())
	fmt.Fprintf(tw, "  Source\t%s\n", entry.Repo)
	fmt.Fprintf(tw, "  Path\t%s\n", entry.Path)
	fmt.Fprintf(tw, "  Author\t%s\n", entry.Author)
	if len(entry.Tags) > 0 {
		fmt.Fprintf(tw, "  Tags\t%s\n", strings.Join(entry.Tags, ", "))
	}
	if entry.Hook != nil {
		event := entry.Hook.Event
		if entry.Hook.Matcher != "" {
			event += " (matcher: " + entry.Hook.Matcher + ")"
		}
		fmt.Fprintf(tw, "  Hook\t%s\n", event)
	}
	if entry.Ref != "" {
		fmt.Fprintf(tw, "  Ref\t%s\n", entry.Ref)
	}
	if entry.Cksum != "" {
		fmt.Fprintf(tw, "  Cksum\t%s\n", entry.Cksum)
	}

	if target, found, ferr := install.FindInstall(entry.Name, *dir); ferr == nil && found {
		if rec, recorded, _ := install.ReadRecord(target, entry.Name); recorded {
			detail := short(rec.Commit)
			if !rec.InstalledAt.IsZero() {
				detail += ", " + rec.InstalledAt.Format("2006-01-02")
			}
			fmt.Fprintf(tw, "  Installed\tyes (%s)\n", detail)
		} else {
			fmt.Fprintf(tw, "  Installed\tyes\n")
		}
	} else {
		fmt.Fprintf(tw, "  Installed\tno\n")
	}
	return tw.Flush()
}

func cmdPublish(_ context.Context, _ []string) error {
	fmt.Print(`Publish a skill to the registry:

  1. Fork github.com/Brattlof/skillet
  2. Add skills/<first-letter>/<name>.json (e.g. skills/g/git-commit.json):

       {
         "name": "your-skill",
         "description": "One line on what it does.",
         "repo": "https://github.com/you/your-repo",
         "path": "path/to/skill-folder",
         "author": "you",
         "tags": ["example"]
       }

  3. Run: go run ./cmd/buildindex -check
  4. Open a PR and tell us how you've used it (the one rule). See CONTRIBUTING.md.
`)
	return nil
}

func printEntries(entries []registry.Entry) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tDESCRIPTION\tTAGS")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, e.KindOrDefault(), truncate(e.Description, 56), strings.Join(e.Tags, ","))
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
