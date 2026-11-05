package trending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Brattlof/skillet/internal/skillssh"
)

// Spam filter thresholds. skills.sh ranks by raw install telemetry, which is
// easy to inflate: in November 2026 most of the trending top 20 came from
// weeks-old repos with under 30 stars reporting 10-30k installs each, several
// of them forks of one another. Real popular skills sit in repos with hundreds
// to hundreds of thousands of stars. Skills marked official by skills.sh skip
// the star checks.
//
// ponytail: star-based heuristic, gameable by buying stars - upgrade to
// install-velocity or audit-history signals if spam starts passing it.
const (
	minStars           = 10 // repos below this are dropped
	maxInstallsPerStar = 50 // trending only: installs above stars*this are dropped
	minRows            = 20 // fewer leaderboard rows than this means the page changed
	maxFailures        = 10 // lookup failures tolerated per view before giving up
)

// Config controls a snapshot build.
type Config struct {
	SkillsSH    *skillssh.Client
	GitHubAPI   string // GitHub REST base URL; default https://api.github.com
	GitHubToken string // optional; raises the GitHub rate limit
	HTTP        *http.Client
	Previous    Snapshot // descriptions are reused from it to save requests
	PerView     int      // items kept per list; default 50
	PerSource   int      // items kept per repo per list, for variety; default 3
	Log         io.Writer
}

type repoInfo struct {
	Stars    int  `json:"stargazers_count"`
	Fork     bool `json:"fork"`
	Archived bool `json:"archived"`
}

type builder struct {
	Config
	repos       map[string]*repoInfo // nil value: repo not found
	descs       map[string]string
	descsPaused bool // set after a rate limit; descriptions are optional
}

// Build fetches the trending and hot leaderboards from skills.sh, filters them,
// and returns a snapshot that Decode accepts.
func Build(ctx context.Context, cfg Config) (Snapshot, error) {
	if cfg.GitHubAPI == "" {
		cfg.GitHubAPI = "https://api.github.com"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.PerView <= 0 {
		cfg.PerView = 50
	}
	if cfg.PerSource <= 0 {
		cfg.PerSource = 3
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	b := &builder{Config: cfg, repos: map[string]*repoInfo{}, descs: map[string]string{}}
	for _, list := range [][]Item{cfg.Previous.Trending, cfg.Previous.Hot} {
		for _, it := range list {
			if it.Description != "" {
				b.descs[it.ID()] = it.Description
			}
		}
	}

	snap := Snapshot{GeneratedAt: time.Now().UTC().Truncate(time.Second)}
	for _, v := range []struct {
		view string
		dst  *[]Item
	}{{skillssh.ViewTrending, &snap.Trending}, {skillssh.ViewHot, &snap.Hot}} {
		rows, err := cfg.SkillsSH.Leaderboard(ctx, v.view)
		if err != nil {
			return Snapshot{}, err
		}
		if len(rows) < minRows {
			return Snapshot{}, fmt.Errorf("skills.sh %s page had only %d rows (want at least %d); the page format may have changed", v.view, len(rows), minRows)
		}
		items, err := b.filter(ctx, v.view, rows)
		if err != nil {
			return Snapshot{}, fmt.Errorf("%s: %w", v.view, err)
		}
		*v.dst = items
	}

	// Round-trip through Decode so CI never publishes a snapshot the CLI rejects.
	raw, err := json.Marshal(snap)
	if err != nil {
		return Snapshot{}, err
	}
	return Decode(raw)
}

func (b *builder) filter(ctx context.Context, view string, rows []skillssh.Skill) ([]Item, error) {
	perSource := map[string]int{}
	dropped := map[string]int{}
	failures := 0
	fail := func(err error) error {
		failures++
		fmt.Fprintf(b.Log, "%s: %v\n", view, err)
		if failures > maxFailures {
			return fmt.Errorf("too many lookup failures (last: %w)", err)
		}
		return nil
	}

	var out []Item
	for _, r := range rows {
		if len(out) == b.PerView {
			break
		}
		if perSource[r.Source] >= b.PerSource {
			dropped["per-repo cap"]++
			continue
		}
		repo, err := b.repo(ctx, r.Source)
		if err != nil {
			if ferr := fail(err); ferr != nil {
				return nil, ferr
			}
			continue
		}
		switch {
		case repo == nil:
			dropped["repo not found"]++
			continue
		case repo.Fork:
			dropped["fork"]++
			continue
		case repo.Archived:
			dropped["archived"]++
			continue
		case !r.Official && repo.Stars < minStars:
			dropped[fmt.Sprintf("under %d stars", minStars)]++
			continue
		case !r.Official && view == skillssh.ViewTrending && r.Installs > int64(max(repo.Stars, 1))*maxInstallsPerStar:
			dropped["installs out of line with stars"]++
			continue
		}

		// Audited only once a row passes the cheaper checks: one request each.
		audit, err := b.SkillsSH.Audit(ctx, r.Source, r.SkillID)
		if err != nil {
			if ferr := fail(fmt.Errorf("audit for %s: %w", r.ID(), err)); ferr != nil {
				return nil, ferr
			}
			continue // unknown audit: leave it out rather than vouch for it
		}
		worst := audit.Worst()
		if worst != "" && skillssh.AtLeast(worst, skillssh.Blocking) {
			dropped["failed audit"]++
			continue
		}

		it := Item{
			Source:   r.Source,
			Skill:    r.SkillID,
			Name:     r.Name,
			Installs: r.Installs,
			Change:   r.Change,
			Stars:    repo.Stars,
			Official: r.Official,
			Audit:    worst,
		}
		it.Description = b.description(ctx, it.ID(), r.Source, r.SkillID)
		perSource[r.Source]++
		out = append(out, it)
	}
	fmt.Fprintf(b.Log, "%s: kept %d of %d rows; dropped %s\n", view, len(out), len(rows), summarize(dropped))
	return out, nil
}

// description returns the skill's SKILL.md description, reusing the previous
// snapshot's copy when there is one. A failed fetch yields "" - a missing
// description is cosmetic, not a reason to drop the skill.
func (b *builder) description(ctx context.Context, id, source, skill string) string {
	if d, ok := b.descs[id]; ok {
		return d
	}
	if b.descsPaused {
		return ""
	}
	md, err := b.SkillsSH.SkillMD(ctx, source, skill)
	if err != nil {
		fmt.Fprintf(b.Log, "description for %s: %v\n", id, err)
		var se *skillssh.StatusError
		if errors.As(err, &se) && se.Code == http.StatusTooManyRequests {
			// Save the remaining budget for audits; the next run fills these in.
			fmt.Fprintln(b.Log, "pausing description fetches for this run")
			b.descsPaused = true
		}
		return ""
	}
	d := skillssh.ParseFrontmatter(md).Description
	b.descs[id] = d
	return d
}

// repo returns GitHub metadata for source, or nil if the repo does not exist.
func (b *builder) repo(ctx context.Context, source string) (*repoInfo, error) {
	if info, ok := b.repos[source]; ok {
		return info, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.GitHubAPI, "/")+"/repos/"+source, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", skillssh.UserAgent)
	if b.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.GitHubToken)
	}
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusUnavailableForLegalReasons:
		b.repos[source] = nil
		return nil, nil
	default:
		return nil, fmt.Errorf("GitHub returned HTTP %d for %s", resp.StatusCode, source)
	}
	var info repoInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return nil, fmt.Errorf("decoding GitHub metadata for %s: %w", source, err)
	}
	if info.Stars < 0 {
		return nil, errors.New("GitHub returned a negative star count")
	}
	b.repos[source] = &info
	return &info, nil
}

func summarize(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", m[k], k)
	}
	return strings.Join(parts, ", ")
}
