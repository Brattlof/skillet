package cli

import (
	"context"
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
	})
	return install.WriteLock(p, lf)
}

// cmdInstall restores from the lockfile when given no skill name, and otherwise
// behaves like add (npm-style).
func cmdInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) >= 1 {
		return cmdAdd(ctx, args)
	}
	return restoreFromLock(ctx, *dir)
}

func restoreFromLock(ctx context.Context, dirOverride string) error {
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

	var n, skipped int
	for _, le := range lf.Skills {
		e := registry.Entry{Name: le.Name, Kind: le.Kind, Repo: le.Repo, Path: le.Path, Ref: le.Commit, Cksum: le.Cksum}
		// The lockfile is an untrusted, shareable artifact, so validate every
		// entry before it reaches git or the filesystem. An invalid or unknown
		// entry is skipped, not fatal, which also keeps restore forward compatible.
		if err := registry.ValidateInstall(e); err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", le.Name, err)
			skipped++
			continue
		}
		target, err := install.TargetDir(le.Kind, dirOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", le.Name, err)
			skipped++
			continue
		}
		dest, err := install.Install(ctx, e, target)
		if err != nil {
			return fmt.Errorf("installing %s: %w", le.Name, err)
		}
		fmt.Printf("Installed %s -> %s\n", le.Name, dest)
		n++
	}
	if skipped > 0 {
		fmt.Printf("\nrestored %d skill(s), skipped %d from %s\n", n, skipped, p)
	} else {
		fmt.Printf("\nrestored %d skill(s) from %s\n", n, p)
	}
	return nil
}

// cmdLock writes the lockfile from the currently installed skills.
func cmdLock(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}

	dirs, err := install.ScanDirs(*dir)
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
