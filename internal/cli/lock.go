package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Brattlof/skillet/internal/install"
	"github.com/Brattlof/skillet/internal/registry"
)

// lockPath is the lockfile location: $SKILLET_LOCKFILE or skillet.lock in the cwd.
func lockPath() string {
	if v := os.Getenv("SKILLET_LOCKFILE"); v != "" {
		return v
	}
	return "skillet.lock"
}

// splitNameRef splits "name@ref" into its parts. A name with no "@" returns an
// empty ref. The "@" must not be the first character so handles are not split.
func splitNameRef(s string) (name, ref string) {
	if i := strings.LastIndex(s, "@"); i > 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// upsertLock records a freshly installed skill in the lockfile.
func upsertLock(rec install.Record) error {
	p := lockPath()
	lf, err := install.ReadLock(p)
	if err != nil {
		return err
	}
	lf.Upsert(install.LockEntry{
		Name:   rec.Name,
		Kind:   rec.Kind,
		Repo:   rec.Repo,
		Path:   rec.Path,
		Commit: rec.Commit,
		Cksum:  rec.Cksum,
		Hook:   rec.Hook,
	})
	return install.WriteLock(p, lf)
}

// cmdInstall restores from the lockfile when given no skill name, and otherwise
// behaves like add (npm-style). With --frozen it verifies the install against the
// lockfile without changing anything.
func cmdInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	tgtFlag := fs.String("target", "", targetUsage)
	frozen := fs.Bool("frozen", false, "verify the install matches the lockfile without changing anything")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) >= 1 {
		if *frozen {
			return errors.New("--frozen restores the whole lockfile; do not pass a skill name")
		}
		return cmdAdd(ctx, args)
	}
	tgt, err := resolveTarget(*tgtFlag)
	if err != nil {
		return err
	}
	if *frozen {
		return verifyLock(tgt, *dir)
	}
	return restoreFromLock(ctx, tgt, *dir)
}

func restoreFromLock(ctx context.Context, target, dirOverride string) error {
	p := lockPath()
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("no lockfile at %s (run: skillet lock)", p)
	}
	lf, err := install.ReadLock(p)
	if err != nil {
		return err
	}
	if len(lf.Skills) == 0 {
		fmt.Printf("%s has no skills\n", p)
		return nil
	}

	locked := make(map[string]bool, len(lf.Skills))
	for _, le := range lf.Skills {
		locked[strings.ToLower(le.Name)] = true
	}

	var n, skipped int
	for _, le := range lf.Skills {
		e := registry.Entry{Name: le.Name, Kind: le.Kind, Repo: le.Repo, Path: le.Path, Ref: le.Commit, Cksum: le.Cksum, Hook: le.Hook}
		// The lockfile is an untrusted, shareable artifact, so validate every
		// entry before it reaches git or the filesystem. An invalid or unknown
		// entry is skipped, not fatal, which also keeps restore forward compatible.
		if err := registry.ValidateInstall(e); err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", le.Name, err)
			skipped++
			continue
		}
		instDir, err := install.TargetDir(le.Kind, target, dirOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", le.Name, err)
			skipped++
			continue
		}
		dest, err := install.Install(ctx, e, instDir)
		if err != nil {
			return fmt.Errorf("installing %s: %w", le.Name, err)
		}
		fmt.Printf("Installed %s -> %s\n", le.Name, dest)
		n++
	}

	// Prune so the install matches the lockfile exactly (npm ci style). Only
	// skillet-managed installs (those with a manifest record) are removed; a
	// hand-placed file is left alone.
	pruned, err := pruneToLock(target, dirOverride, locked)
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("\nrestored %d skill(s)", n)
	if skipped > 0 {
		summary += fmt.Sprintf(", skipped %d", skipped)
	}
	if pruned > 0 {
		summary += fmt.Sprintf(", pruned %d", pruned)
	}
	fmt.Printf("%s from %s\n", summary, p)
	return nil
}

// pruneToLock removes every skillet-managed install whose name is not in locked,
// across all kind directories, and returns how many it removed.
func pruneToLock(target, dirOverride string, locked map[string]bool) (int, error) {
	dirs, err := install.ScanDirs(target, dirOverride)
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, dk := range dirs {
		recs, err := install.Records(dk.Dir)
		if err != nil {
			return pruned, err
		}
		for _, r := range recs {
			if locked[strings.ToLower(r.Name)] {
				continue
			}
			if err := install.Remove(r.Name, dk.Dir); err != nil {
				return pruned, fmt.Errorf("pruning %s: %w", r.Name, err)
			}
			fmt.Printf("Removed %s (not in %s)\n", r.Name, lockPath())
			pruned++
		}
	}
	return pruned, nil
}

// verifyLock reports whether the installed artifacts match the lockfile, without
// changing anything. It fails if a locked entry is missing or has drifted, or if a
// skillet-managed install is not in the lockfile.
func verifyLock(target, dirOverride string) error {
	p := lockPath()
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("no lockfile at %s (run: skillet lock)", p)
	}
	lf, err := install.ReadLock(p)
	if err != nil {
		return err
	}

	locked := make(map[string]bool, len(lf.Skills))
	var problems int
	for _, le := range lf.Skills {
		instDir, err := install.TargetDir(le.Kind, target, dirOverride)
		if err != nil {
			// Not routable for this target (e.g. a Claude-only kind under
			// --target agents). Restore skips these, so verify must agree.
			fmt.Printf("skip  %s: not installed under the %s target\n", le.Name, target)
			continue
		}
		locked[strings.ToLower(le.Name)] = true
		match, ok, err := install.VerifyChecksum(instDir, le.Name, le.Cksum)
		if err != nil {
			return err
		}
		switch {
		case !ok:
			fmt.Printf("FAIL  %s: not installed\n", le.Name)
			problems++
		case le.Cksum != "" && !match:
			fmt.Printf("FAIL  %s: content differs from the lockfile\n", le.Name)
			problems++
		default:
			fmt.Printf("ok    %s\n", le.Name)
		}
	}

	dirs, err := install.ScanDirs(target, dirOverride)
	if err != nil {
		return err
	}
	for _, dk := range dirs {
		recs, err := install.Records(dk.Dir)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if !locked[strings.ToLower(r.Name)] {
				fmt.Printf("FAIL  %s: installed but not in the lockfile\n", r.Name)
				problems++
			}
		}
	}

	if problems > 0 {
		fmt.Printf("\n%d problem(s); install does not match %s\n", problems, p)
		return errSilent
	}
	fmt.Printf("\ninstall matches %s\n", p)
	return nil
}

// cmdLock writes the lockfile from the currently installed skills.
func cmdLock(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	tgtFlag := fs.String("target", "", targetUsage)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	tgt, err := resolveTarget(*tgtFlag)
	if err != nil {
		return err
	}

	dirs, err := install.ScanDirs(tgt, *dir)
	if err != nil {
		return err
	}
	var lf install.Lockfile
	for _, dk := range dirs {
		recs, err := install.Records(dk.Dir)
		if err != nil {
			return err
		}
		for _, r := range recs {
			lf.Upsert(install.LockEntry{
				Name:   r.Name,
				Kind:   r.Kind,
				Repo:   r.Repo,
				Path:   r.Path,
				Commit: r.Commit,
				Cksum:  r.Cksum,
				Hook:   r.Hook,
			})
		}
	}

	p := lockPath()
	if err := install.WriteLock(p, lf); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d skill(s))\n", p, len(lf.Skills))
	return nil
}
