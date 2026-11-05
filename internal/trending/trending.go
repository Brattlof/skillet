// Package trending holds skillet's snapshot of the skills.sh trending and hot
// leaderboards. CI builds the snapshot hourly (Build), filters out spam, and
// publishes it next to the registry index; the CLI reads it (Load) so users never
// hit skills.sh for a leaderboard and the list works offline from the cache.
package trending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Brattlof/skillet/internal/httpcache"
	"github.com/Brattlof/skillet/internal/skillssh"
)

// DefaultURL serves the snapshot from the gh-pages branch, next to the index.
const DefaultURL = "https://raw.githubusercontent.com/Brattlof/skillet/gh-pages/trending.json"

const (
	cacheName = "trending.json"
	cacheTTL  = 30 * time.Minute
	// maxItems bounds each list in a snapshot read from the network.
	maxItems = 500
	// maxDescription bounds a description, in runes.
	maxDescription = 300
)

// Snapshot is the published leaderboard data.
type Snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	Trending    []Item    `json:"trending"`
	Hot         []Item    `json:"hot"`
}

// Item is one leaderboard row that passed the filters.
type Item struct {
	Source      string `json:"source"` // GitHub owner/repo
	Skill       string `json:"skill"`  // skills.sh skillId (slug)
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Installs    int64  `json:"installs"`         // trending: installs in the window; hot: this hour
	Change      int64  `json:"change,omitempty"` // hot: this hour minus the same hour yesterday
	Stars       int    `json:"stars"`
	Official    bool   `json:"official,omitempty"`
	Audit       string `json:"audit,omitempty"` // worst skills.sh audit verdict (pass, warn, fail), "" if not audited
}

// ID is the item's install spec: owner/repo/skill.
func (i Item) ID() string { return i.Source + "/" + i.Skill }

// Decode parses a snapshot and validates every item. The snapshot arrives from
// the network or the cache, so a bad item rejects the whole snapshot and the
// caller falls back to the previous good copy.
func Decode(b []byte) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, fmt.Errorf("parsing trending snapshot: %w", err)
	}
	if s.GeneratedAt.IsZero() {
		return Snapshot{}, errors.New("trending snapshot has no generated_at")
	}
	if len(s.Trending) > maxItems || len(s.Hot) > maxItems {
		return Snapshot{}, fmt.Errorf("trending snapshot has more than %d items in a list", maxItems)
	}
	for _, list := range [][]Item{s.Trending, s.Hot} {
		for i := range list {
			if err := clean(&list[i]); err != nil {
				return Snapshot{}, err
			}
		}
	}
	return s, nil
}

// clean validates an item's identifiers and makes its text terminal-safe.
func clean(it *Item) error {
	if !skillssh.ValidSource(it.Source) || !skillssh.ValidSlug(it.Skill) {
		return fmt.Errorf("trending item %q/%q has an invalid source or skill", it.Source, it.Skill)
	}
	if it.Installs < 0 || it.Stars < 0 {
		return fmt.Errorf("trending item %s has a negative count", it.ID())
	}
	it.Name = skillssh.Clean(it.Name)
	if it.Name == "" {
		it.Name = it.Skill
	}
	it.Description = truncate(skillssh.Clean(it.Description), maxDescription)
	it.Audit = skillssh.Clean(it.Audit)
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// URL is $SKILLET_TRENDING_URL or the published snapshot.
func URL() string {
	if v := os.Getenv("SKILLET_TRENDING_URL"); v != "" {
		return v
	}
	return DefaultURL
}

// ErrUnavailable means there is no snapshot: the fetch failed and nothing is
// cached (or skillet is offline with an empty cache).
var ErrUnavailable = errors.New("the trending list is unavailable (no network and no cached copy)")

// Load returns the published snapshot, from a fresh cache when possible.
func Load(ctx context.Context) (Snapshot, error) {
	validate := func(b []byte) error {
		_, err := Decode(b)
		return err
	}
	body, ok := httpcache.Get(ctx, httpClient, URL(), cacheName, cacheTTL, validate)
	if !ok {
		return Snapshot{}, ErrUnavailable
	}
	return Decode(body)
}
