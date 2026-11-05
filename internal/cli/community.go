package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Brattlof/skillet/internal/httpcache"
	"github.com/Brattlof/skillet/internal/install"
	"github.com/Brattlof/skillet/internal/registry"
	"github.com/Brattlof/skillet/internal/skillssh"
	"github.com/Brattlof/skillet/internal/trending"
)

// communityLimit is how many skills.sh results search shows after the registry's.
const communityLimit = 10

// communityEntry is the install entry for a skills.sh skill. Its path is found in
// the cloned repo at install time, so it is empty here.
func communityEntry(source, skill, ref string) registry.Entry {
	return registry.Entry{Name: skill, Repo: skillssh.RepoURL(source), Ref: ref}
}

// validateCommunity runs the registry's install checks (safe name, https repo,
// git-safe ref) on a community entry. Its path is found after cloning, so a
// placeholder stands in for it here.
func validateCommunity(e registry.Entry) error {
	e.Path = "."
	return registry.ValidateInstall(e)
}

// sourceFromRepo recovers owner/repo from a recorded community install's repo URL,
// validating it: records and lockfiles are local files anyone could have edited.
func sourceFromRepo(repo string) (string, bool) {
	s, ok := strings.CutPrefix(repo, "https://github.com/")
	return s, ok && skillssh.ValidSource(s)
}

// addCommunity installs a skill from skills.sh (any GitHub-hosted SKILL.md). It
// shows the skills.sh security audit first and refuses a failed one
// unless force is set.
func addCommunity(ctx context.Context, source, skill, ref, tgtFlag, dirFlag string, force, replace bool) error {
	if !skillssh.ValidSource(source) || !skillssh.ValidSlug(skill) {
		return fmt.Errorf("invalid community skill %q", source+"/"+skill)
	}
	tgt, err := resolveTarget(tgtFlag)
	if err != nil {
		return err
	}
	entry := communityEntry(source, skill, ref)
	if err := validateCommunity(entry); err != nil {
		return err
	}
	id := source + "/" + skill

	// skills.sh audits a skill's current version only; it cannot vouch for an
	// arbitrary ref, which could be an old or never-released bad commit. The
	// lockfile already pins the exact commit of a normal install.
	if ref != "" {
		if !force {
			return fmt.Errorf("the skills.sh audit covers the current version of %s, not %q; install without @ref (skillet.lock pins the exact commit), or pass --force", id, ref)
		}
		fmt.Fprintf(os.Stderr, "warning: the skills.sh audit covers the current version of %s, not %s (--force)\n", id, ref)
	}
	if err := checkAudit(ctx, source, skill, force); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "warning: %s is a community skill from skills.sh, not reviewed by skillet; read its SKILL.md before you rely on it\n", id)

	instDir, err := install.TargetDir("skill", tgt, dirFlag)
	if err != nil {
		return err
	}
	warnReplacing(instDir, entry)
	fmt.Printf("Installing %s from %s ...\n", id, entry.Repo)
	dest, err := install.InstallWith(ctx, entry, instDir, install.Options{Source: skillssh.SourceName, Discover: skill, Replace: replace})
	if err != nil {
		return err
	}
	rec, recorded, _ := install.ReadRecord(instDir, entry.Name)
	if recorded && rec.Path != "." {
		fmt.Printf("Installed %s -> %s (from %s)\n", id, dest, rec.Path)
	} else {
		fmt.Printf("Installed %s -> %s\n", id, dest)
	}
	if recorded {
		if err := upsertLock(rec); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not update", lockPath()+":", err)
		}
	}
	return nil
}

// checkAudit prints the skills.sh audit for a skill and returns an error when it
// blocks the install. An unavailable audit is a warning, not a block: skills.sh is
// a convenience, and a new skill may not have been scanned yet.
func checkAudit(ctx context.Context, source, skill string, force bool) error {
	if httpcache.Offline() {
		fmt.Fprintln(os.Stderr, "note: offline, so the skills.sh security audit was not checked")
		return nil
	}
	a, err := skillssh.New().Audit(ctx, source, skill)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not fetch the skills.sh security audit: %v\n", err)
		return nil
	}
	if len(a) == 0 {
		fmt.Fprintln(os.Stderr, "warning: skills.sh has no security audit for this skill yet")
		return nil
	}
	fmt.Fprintf(os.Stderr, "audit: %s\n", a)
	if skillssh.AtLeast(a.Worst(), skillssh.Blocking) {
		if !force {
			return fmt.Errorf("%s/%s fails its skills.sh security audit; see %s, or re-run with --force to install anyway",
				source, skill, skillssh.PageURL(source, skill))
		}
		fmt.Fprintln(os.Stderr, "warning: installing despite a failed security audit (--force)")
	}
	return nil
}

// warnReplacing notes when an install will overwrite an existing one that came
// from a different repository, such as a community skill sharing a registry name.
func warnReplacing(dir string, e registry.Entry) {
	if prev, ok, err := install.ReadRecord(dir, e.Name); err == nil && ok && prev.Repo != "" && prev.Repo != e.Repo {
		fmt.Fprintf(os.Stderr, "warning: replacing %s, currently installed from %s\n", e.Name, prev.Repo)
	}
}

// searchCommunity prints skills.sh results for query, leaving out any skill the
// registry already listed. It is best-effort: a failure prints a note and
// returns, so registry search still works when skills.sh is down.
func searchCommunity(ctx context.Context, query string, registryHits []registry.Entry) (shown int) {
	if httpcache.Offline() {
		return 0
	}
	results, err := skillssh.New().Search(ctx, query, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: skills.sh search unavailable: %v\n", err)
		return 0
	}
	listed := map[string]bool{}
	for _, e := range registryHits {
		listed[strings.ToLower(e.Repo)+"#"+strings.ToLower(e.Name)] = true
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, s := range results {
		if listed[strings.ToLower(skillssh.RepoURL(s.Source))+"#"+s.SkillID] {
			continue
		}
		if shown == 0 {
			if len(registryHits) > 0 {
				fmt.Fprintln(tw)
			}
			fmt.Fprintln(tw, "From skills.sh (community, not reviewed by skillet):")
			fmt.Fprintln(tw, "SKILL\tINSTALLS")
		}
		fmt.Fprintf(tw, "%s\t%s\n", s.ID(), humanCount(s.Installs))
		if shown++; shown == communityLimit {
			break
		}
	}
	_ = tw.Flush()
	if shown > 0 {
		fmt.Println("\nInstall a community skill with: skillet add <owner/repo/skill>")
	}
	return shown
}

// infoCommunity shows a skills.sh skill: its description from the skills.sh
// snapshot, its audit, and whether it is installed.
func infoCommunity(ctx context.Context, source, skill, tgtFlag, dirFlag string) error {
	tgt, err := resolveTarget(tgtFlag)
	if err != nil {
		return err
	}
	id := source + "/" + skill
	fmt.Println(id)
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	if !httpcache.Offline() {
		c := skillssh.New()
		if md, err := c.SkillMD(ctx, source, skill); err == nil && md != "" {
			if d := skillssh.ParseFrontmatter(md).Description; d != "" {
				fmt.Fprintf(tw, "  Description\t%s\n", truncate(d, 300))
			}
		}
		if a, err := c.Audit(ctx, source, skill); err == nil {
			if len(a) == 0 {
				fmt.Fprintf(tw, "  Audit\tnone on skills.sh yet\n")
			}
			for i, f := range a {
				label := ""
				if i == 0 {
					label = "Audit"
				}
				line := f.Provider + ": " + f.Status
				if f.Summary != "" {
					line += " - " + truncate(f.Summary, 70)
				}
				fmt.Fprintf(tw, "  %s\t%s\n", label, line)
			}
		}
	}
	fmt.Fprintf(tw, "  Kind\tskill (community, not reviewed by skillet)\n")
	fmt.Fprintf(tw, "  Source\t%s\n", skillssh.RepoURL(source))
	fmt.Fprintf(tw, "  Page\t%s\n", skillssh.PageURL(source, skill))
	fmt.Fprintf(tw, "  Install\tskillet add %s\n", id)
	installed := "no"
	if instDir, found, ferr := install.FindInstall(skill, tgt, dirFlag); ferr == nil && found {
		installed = "yes"
		if rec, ok, _ := install.ReadRecord(instDir, skill); ok {
			if rec.Repo != skillssh.RepoURL(source) {
				installed = "no (" + skill + " is installed from " + rec.Repo + ")"
			} else {
				installed = "yes (" + short(rec.Commit) + ")"
			}
		}
	}
	fmt.Fprintf(tw, "  Installed\t%s\n", installed)
	return tw.Flush()
}

// cmdTrending prints the trending (or, with hot, the hot) skills from the
// published snapshot.
func cmdTrending(ctx context.Context, args []string, hot bool) error {
	name := "trending"
	if hot {
		name = "hot"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	limit := fs.Int("limit", 20, "how many skills to show (max 50)")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *limit < 1 {
		return errors.New("--limit must be at least 1")
	}
	snap, err := trending.Load(ctx)
	if err != nil {
		return err
	}
	items, what := snap.Trending, "Trending skills on skills.sh"
	if hot {
		items, what = snap.Hot, "Hot on skills.sh (installs in the last hour)"
	}
	if len(items) == 0 {
		fmt.Println("No skills to show right now")
		return nil
	}
	if len(items) > *limit {
		items = items[:*limit]
	}

	age := time.Since(snap.GeneratedAt)
	fmt.Printf("%s - updated %s\n", what, ago(age))
	fmt.Println("Community skills, not reviewed by skillet. Spam-filtered, but check a skill before you install it.")
	if age > 6*time.Hour {
		fmt.Fprintf(os.Stderr, "warning: this list was last updated %s; the hourly update may be failing\n", ago(age))
	}
	fmt.Println()

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	if hot {
		fmt.Fprintln(tw, "#\tSKILL\tLAST HOUR\tAUDIT\tDESCRIPTION")
	} else {
		fmt.Fprintln(tw, "#\tSKILL\tINSTALLS\tAUDIT\tDESCRIPTION")
	}
	for i, it := range items {
		count := humanCount(it.Installs)
		if hot && it.Change > 0 {
			count += " (+" + humanCount(it.Change) + ")"
		}
		audit := it.Audit
		if audit == "" {
			audit = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", i+1, it.ID(), count, audit, truncate(it.Description, 56))
	}
	_ = tw.Flush()
	fmt.Println("\nInstall one with: skillet add <owner/repo/skill>")
	return nil
}

// humanCount renders 17625 as 17.6k.
func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprint(n)
	}
}

// ago renders a duration coarsely: "just now", "12 min ago", "3 h ago", "2 days ago".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
