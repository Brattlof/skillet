package trending

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brattlof/skillet/internal/skillssh"
)

func validSnapshot() Snapshot {
	return Snapshot{
		GeneratedAt: time.Now().UTC(),
		Trending:    []Item{{Source: "a/b", Skill: "c", Name: "c", Installs: 5, Stars: 100}},
		Hot:         []Item{{Source: "a/b", Skill: "d", Name: "d", Installs: 1, Change: 1, Stars: 100}},
	}
}

func TestDecode(t *testing.T) {
	b, _ := json.Marshal(validSnapshot())
	if _, err := Decode(b); err != nil {
		t.Fatal(err)
	}

	bad := map[string]func(*Snapshot){
		"no time":         func(s *Snapshot) { s.GeneratedAt = time.Time{} },
		"bad source":      func(s *Snapshot) { s.Trending[0].Source = "../etc" },
		"web source":      func(s *Snapshot) { s.Hot[0].Source = "uizze.sh" },
		"bad skill":       func(s *Snapshot) { s.Trending[0].Skill = "../.bashrc" },
		"negative count":  func(s *Snapshot) { s.Trending[0].Installs = -1 },
		"negative stars":  func(s *Snapshot) { s.Hot[0].Stars = -1 },
		"too many items":  func(s *Snapshot) { s.Trending = make([]Item, maxItems+1) },
		"option-like src": func(s *Snapshot) { s.Trending[0].Source = "-u/x" },
	}
	for name, mutate := range bad {
		s := validSnapshot()
		mutate(&s)
		b, _ := json.Marshal(s)
		if _, err := Decode(b); err == nil {
			t.Errorf("%s: expected the snapshot to be rejected", name)
		}
	}
	if _, err := Decode([]byte("{")); err == nil {
		t.Error("malformed JSON must be rejected")
	}
}

func TestDecodeCleansText(t *testing.T) {
	s := validSnapshot()
	s.Trending[0].Name = "\x1b]0;pwned\x07name"
	s.Trending[0].Description = "\x1b[2J" + strings.Repeat("é", 400)
	s.Trending[0].Audit = "low\x1b[0m"
	s.Hot[0].Name = ""
	b, _ := json.Marshal(s)
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	it := got.Trending[0]
	if strings.ContainsRune(it.Name+it.Description+it.Audit, 0x1b) || strings.ContainsRune(it.Name, 0x07) {
		t.Fatalf("control characters survived: %q %q %q", it.Name, it.Description, it.Audit)
	}
	if n := len([]rune(it.Description)); n != maxDescription {
		t.Fatalf("description is %d runes, want %d", n, maxDescription)
	}
	if got.Hot[0].Name != "d" {
		t.Fatalf("empty name should fall back to the skill, got %q", got.Hot[0].Name)
	}
}

// leaderboardPage renders rows the way skills.sh embeds them in its pages.
func leaderboardPage(rows []map[string]any) string {
	b, _ := json.Marshal(map[string]any{"initialSkills": rows})
	chunk, _ := json.Marshal(string(b))
	return `<script>self.__next_f.push([1,` + string(chunk) + `])</script>`
}

type fakeWorld struct {
	trending, hot []map[string]any
	repos         map[string]repoInfo // missing: 404
	audits        map[string]string   // "source/skill" -> verdict; default pass
	auditFail     map[string]bool     // sources whose audit request fails
	descs         map[string]string   // "source/skill" -> description
	descStatus    int                 // non-zero: every download returns this
	githubStatus  int                 // non-zero: every repo lookup returns this
	downloads     atomic.Int32
}

func (w *fakeWorld) serve(t *testing.T) (skillsURL, githubURL string) {
	t.Helper()
	sk := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/trending":
			fmt.Fprint(rw, leaderboardPage(w.trending))
		case r.URL.Path == "/hot":
			fmt.Fprint(rw, leaderboardPage(w.hot))
		case strings.HasPrefix(r.URL.Path, "/api/v1/skills/audit/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/skills/audit/")
			src := id[:strings.LastIndex(id, "/")]
			if w.auditFail[src] {
				rw.WriteHeader(503)
				return
			}
			status := w.audits[id]
			if status == "" {
				status = "pass"
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{"audits": []map[string]string{{"provider": "Snyk", "status": status, "summary": "s"}}})
		case strings.HasPrefix(r.URL.Path, "/api/download/"):
			w.downloads.Add(1)
			if w.descStatus != 0 {
				rw.WriteHeader(w.descStatus)
				return
			}
			id := strings.TrimPrefix(r.URL.Path, "/api/download/")
			md := "---\nname: x\ndescription: " + w.descs[id] + "\n---"
			_ = json.NewEncoder(rw).Encode(map[string]any{"files": []map[string]string{{"path": "SKILL.md", "contents": md}}})
		default:
			rw.WriteHeader(404)
		}
	}))
	gh := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if w.githubStatus != 0 {
			rw.WriteHeader(w.githubStatus)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("GitHub token not sent: %q", got)
		}
		info, ok := w.repos[strings.TrimPrefix(r.URL.Path, "/repos/")]
		if !ok {
			rw.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(rw).Encode(info)
	}))
	t.Cleanup(sk.Close)
	t.Cleanup(gh.Close)
	return sk.URL, gh.URL
}

func row(source, skill string, installs int64, official bool) map[string]any {
	r := map[string]any{"source": source, "skillId": skill, "name": skill, "installs": installs}
	if official {
		r["isOfficial"] = true
	}
	return r
}

// padding returns n harmless rows so a view clears minRows.
func padding(n int) []map[string]any {
	var rows []map[string]any
	for i := 0; i < n; i++ {
		rows = append(rows, row(fmt.Sprintf("pad%d/r", i), "p", 1, false))
	}
	return rows
}

func padRepos(m map[string]repoInfo, n int) {
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("pad%d/r", i)] = repoInfo{Stars: 1000}
	}
}

func build(t *testing.T, w *fakeWorld, cfg Config) (Snapshot, error) {
	t.Helper()
	skURL, ghURL := w.serve(t)
	t.Setenv("SKILLET_SKILLSSH_URL", skURL)
	cfg.SkillsSH = skillssh.New()
	cfg.GitHubAPI = ghURL
	cfg.GitHubToken = "tok"
	return Build(context.Background(), cfg)
}

func ids(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID())
	}
	return out
}

func TestBuildFilters(t *testing.T) {
	w := &fakeWorld{
		repos: map[string]repoInfo{
			"real/popular":  {Stars: 50000},
			"spam/clone":    {Stars: 5},
			"fork/copy":     {Stars: 9000, Fork: true},
			"old/archived":  {Stars: 9000, Archived: true},
			"inflated/repo": {Stars: 100}, // 100 stars cannot carry 20k installs
			"official/tiny": {Stars: 3},   // official skips the star checks
			"risky/repo":    {Stars: 9000},
			"flaky/audit":   {Stars: 9000},
			"many/skills":   {Stars: 9000},
		},
		audits:    map[string]string{"risky/repo/bad": "fail", "risky/repo/meh": "warn"},
		auditFail: map[string]bool{"flaky/audit": true},
		descs:     map[string]string{"real/popular/a": "Does a thing."},
	}
	padRepos(w.repos, 20)
	w.trending = append([]map[string]any{
		row("real/popular", "a", 20000, false),
		row("spam/clone", "s", 29000, false),
		row("fork/copy", "f", 15000, false),
		row("old/archived", "o", 15000, false),
		row("inflated/repo", "i", 20000, false),
		row("official/tiny", "t", 9000, true),
		row("gone/repo", "g", 9000, false),
		row("risky/repo", "bad", 8000, false),
		row("risky/repo", "meh", 8000, false),
		row("flaky/audit", "x", 8000, false),
		row("many/skills", "1", 7000, false),
		row("many/skills", "2", 7000, false),
		row("many/skills", "3", 7000, false),
		row("many/skills", "4", 7000, false),
		row("uizze.sh", "web", 7000, false), // non-GitHub: dropped while parsing
	}, padding(20)...)
	// Hot counts are per hour, so the installs-per-star check does not apply.
	w.hot = append([]map[string]any{row("inflated/repo", "i", 900, false)}, padding(20)...)

	snap, err := build(t, w, Config{PerView: 12})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"real/popular/a", "official/tiny/t", "risky/repo/meh", "many/skills/1", "many/skills/2", "many/skills/3"}
	got := ids(snap.Trending)
	if strings.Join(got[:len(want)], " ") != strings.Join(want, " ") {
		t.Fatalf("trending = %v\nwant prefix %v", got, want)
	}
	if len(got) != 12 {
		t.Fatalf("PerView not applied: %d items", len(got))
	}
	if snap.Trending[0].Description != "Does a thing." || snap.Trending[0].Stars != 50000 {
		t.Fatalf("item not filled in: %+v", snap.Trending[0])
	}
	if snap.Trending[2].Audit != "warn" {
		t.Fatalf("a warn audit should be kept and shown: %+v", snap.Trending[2])
	}
	if snap.Hot[0].ID() != "inflated/repo/i" {
		t.Fatalf("hot = %v", ids(snap.Hot))
	}
	if snap.GeneratedAt.IsZero() {
		t.Fatal("no generated_at")
	}
}

func TestBuildReusesPreviousDescriptions(t *testing.T) {
	w := &fakeWorld{repos: map[string]repoInfo{}, descs: map[string]string{}}
	padRepos(w.repos, 20)
	w.trending, w.hot = padding(20), padding(20)
	prev := Snapshot{GeneratedAt: time.Now(), Trending: []Item{}}
	for i := 0; i < 20; i++ {
		prev.Hot = append(prev.Hot, Item{Source: fmt.Sprintf("pad%d/r", i), Skill: "p", Description: "cached"})
	}
	snap, err := build(t, w, Config{Previous: prev})
	if err != nil {
		t.Fatal(err)
	}
	if n := w.downloads.Load(); n != 0 {
		t.Fatalf("made %d description requests; all were cached", n)
	}
	if snap.Trending[0].Description != "cached" {
		t.Fatalf("description not reused: %+v", snap.Trending[0])
	}
}

func TestBuildPausesDescriptionsWhenRateLimited(t *testing.T) {
	w := &fakeWorld{repos: map[string]repoInfo{}, descStatus: http.StatusTooManyRequests}
	padRepos(w.repos, 20)
	w.trending, w.hot = padding(20), padding(20)
	snap, err := build(t, w, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if n := w.downloads.Load(); n != 1 {
		t.Fatalf("made %d description requests after a 429; want 1", n)
	}
	if len(snap.Trending) != 20 {
		t.Fatalf("missing descriptions must not drop items: %d", len(snap.Trending))
	}
}

func TestBuildFailsLoudly(t *testing.T) {
	t.Run("too few rows", func(t *testing.T) {
		w := &fakeWorld{repos: map[string]repoInfo{}}
		padRepos(w.repos, 5)
		w.trending, w.hot = padding(5), padding(20)
		if _, err := build(t, w, Config{}); err == nil || !strings.Contains(err.Error(), "page format") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("GitHub down", func(t *testing.T) {
		w := &fakeWorld{repos: map[string]repoInfo{}, githubStatus: http.StatusForbidden}
		w.trending, w.hot = padding(30), padding(30)
		if _, err := build(t, w, Config{}); err == nil || !strings.Contains(err.Error(), "too many lookup failures") {
			t.Fatalf("err = %v", err)
		}
	})
}
