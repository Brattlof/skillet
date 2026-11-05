package skillssh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
)

func TestValidSource(t *testing.T) {
	good := []string{"anthropics/skills", "vercel-labs/agent-skills", "owner/.github", "a/b.c", "a/b_c", "A1/x", strings.Repeat("a", 39) + "/r"}
	bad := []string{
		"", "uizze.sh", "open.feishu.cn", "a/", "/b", "a/b/c", "../x", "a/..", "a/.", "-a/b", "a-/b",
		"a/-b", "a b/c", "a/b c", "a/b\x00", "a/b\n", strings.Repeat("a", 40) + "/r", "a/" + strings.Repeat("r", 101),
		"ö/x", "a/ö", "a/b?x", "a/b#x", "a/b%2f", "a\\b/c",
	}
	for _, s := range good {
		if !ValidSource(s) {
			t.Errorf("ValidSource(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidSource(s) {
			t.Errorf("ValidSource(%q) = true, want false", s)
		}
	}
}

func TestValidSlug(t *testing.T) {
	for _, s := range []string{"pdf", "find-skills", "a.b", "a_b", "x1", "a..b"} {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false", s)
		}
	}
	for _, s := range []string{"", ".", "..", ".hidden", "-x", "_x", "a/b", `a\b`, "A", "a b", "a\x00", strings.Repeat("a", 129)} {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true", s)
		}
	}
}

// The slug must match the skills CLI's toSkillSlug exactly, or a skillId from
// skills.sh would not find its folder.
func TestSlugMatchesSkillsCLI(t *testing.T) {
	cases := map[string]string{
		"Find Skills":       "find-skills",
		"my_skill":          "my-skill",
		"--a--b--":          "a-b",
		"a.b":               "ab",
		"  x  ":             "x",
		"A\u00a0B":          "a-b",
		"Café Ünïcode":      "caf-ncode",
		"a - b":             "a-b",
		"a-.-b":             "a-b",
		"__init__":          "init",
		"React 19 (beta)!":  "react-19-beta",
		"":                  "",
		"!!!":               "",
		"hyperframes-cli":   "hyperframes-cli",
		"Tab\tand\nnewline": "tab-and-newline",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSpec(t *testing.T) {
	type res struct{ source, skill string }
	good := map[string]res{
		"anthropics/skills/pdf":                       {"anthropics/skills", "pdf"},
		"https://skills.sh/anthropics/skills/pdf":     {"anthropics/skills", "pdf"},
		"https://www.skills.sh/anthropics/skills/pdf": {"anthropics/skills", "pdf"},
		"skills.sh/vercel-labs/skills/find-skills":    {"vercel-labs/skills", "find-skills"},
		"HTTPS://SKILLS.SH/a/b/c":                     {"a/b", "c"},
		"Owner/Repo/My-Skill":                         {"Owner/Repo", "my-skill"},
	}
	for in, want := range good {
		src, sk, ok := ParseSpec(in)
		if !ok || src != want.source || sk != want.skill {
			t.Errorf("ParseSpec(%q) = (%q, %q, %v), want (%q, %q, true)", in, src, sk, ok, want.source, want.skill)
		}
	}
	bad := []string{
		"pdf", "a/b", "a/b/c/d", "a/b/", "/a/b", "a//b", "../b/c", "a/../c", "a/b/..", "a/b/.", "a/b/.git",
		"a/b/-rf", "-a/b/c", "a/-b/c", "uizze.sh/ui-taste", "uizze.sh/x/ui-taste", "a/b/c?x=1", "a/b/c#x",
		"https://github.com/a/b", "a/b/c d", "a\x00/b/c", "a/b/c\n", "https://evil.example/a/b/c",
	}
	for _, in := range bad {
		if src, sk, ok := ParseSpec(in); ok {
			t.Errorf("ParseSpec(%q) = (%q, %q, true), want rejected", in, src, sk)
		}
	}
}

func TestCleanStripsTerminalControl(t *testing.T) {
	cases := map[string]string{
		"\x1b[2J\x1b[31mred\x1b[0m": "[2J[31mred[0m", // ESC removed, so nothing is interpreted
		"a\u009b31mb":               "a31mb",         // C1 CSI introducer
		"evil\u202egnp.exe":         "evilgnp.exe",   // bidi override
		"line1\nline2\r\n\tx":       "line1 line2 x",
		"  lead and trail  ":        "lead and trail",
		"bell\x07 del\x7f nul\x00":  "bell del nul",
		"sep\u2028para\u2029end":    "sepparaend",
		"isolate\u2066x\u2069":      "isolatex",
		"replacement\ufffdchar":     "replacementchar",
		"ünïcode 漢字 emoji 😀 stays":  "ünïcode 漢字 emoji 😀 stays",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("SKILLET_SKILLSSH_URL", srv.URL)
	return New()
}

func TestSearchValidatesAndCleans(t *testing.T) {
	var gotQ, gotUA string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotQ, gotUA = r.URL.Query().Get("q"), r.Header.Get("User-Agent")
		fmt.Fprint(w, `{"skills":[
			{"source":"anthropics/skills","skillId":"pdf","name":"pdf","installs":10},
			{"source":"uizze.sh","skillId":"ui-taste","name":"ui-taste","installs":9},
			{"source":"../../etc","skillId":"passwd","name":"x","installs":8},
			{"source":"a/b","skillId":"../../.bashrc","name":"x","installs":7},
			{"source":"-oProxyCommand=x/y","skillId":"s","name":"x","installs":6},
			{"source":"good/repo","skillId":"ansi","name":"\u001b[2Jcleared","installs":-5},
			{"source":"good/repo","skillId":"noname","name":"","installs":1}
		]}`)
	})
	got, err := c.Search(context.Background(), "pdf & more", 50)
	if err != nil {
		t.Fatal(err)
	}
	if gotQ != "pdf & more" {
		t.Errorf("query not encoded faithfully: got %q", gotQ)
	}
	if gotUA != UserAgent {
		t.Errorf("User-Agent = %q", gotUA)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID())
	}
	if strings.Join(ids, " ") != "anthropics/skills/pdf good/repo/ansi good/repo/noname" {
		t.Fatalf("unexpected results %v", ids)
	}
	if got[1].Name != "[2Jcleared" || got[1].Installs != 0 {
		t.Errorf("row not cleaned: %+v", got[1])
	}
	if got[2].Name != "noname" {
		t.Errorf("empty name should fall back to the slug, got %q", got[2].Name)
	}
}

func TestSearchShortQueryMakesNoRequest(t *testing.T) {
	var hits atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) })
	for _, q := range []string{"", " ", "a", " é "} {
		if res, err := c.Search(context.Background(), q, 10); err != nil || res != nil {
			t.Errorf("Search(%q) = %v, %v; want nil, nil", q, res, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("short queries hit the server %d times", hits.Load())
	}
}

func TestSearchLimit(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "3" {
			t.Errorf("limit param = %q", r.URL.Query().Get("limit"))
		}
		var rows []string
		for i := 0; i < 10; i++ {
			rows = append(rows, fmt.Sprintf(`{"source":"a/b","skillId":"s%d","name":"s","installs":1}`, i))
		}
		fmt.Fprintf(w, `{"skills":[%s]}`, strings.Join(rows, ","))
	})
	got, err := c.Search(context.Background(), "skill", 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d results, err %v; want 3", len(got), err)
	}
}

func TestClientErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"rate limited", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(http.StatusTooManyRequests)
		}, "rate limit reached; retry in 42s"},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, "HTTP 500"},
		{"bad json", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"skills":[`) }, "decoding"},
		{"wrong shape", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"skills":"nope"}`) }, "decoding"},
		{"oversized", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"skills":[],"pad":"`)
			_, _ = w.Write([]byte(strings.Repeat("x", maxJSONBody+10)))
			fmt.Fprint(w, `"}`)
		}, "exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, tc.handler)
			_, err := c.Search(context.Background(), "query", 10)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestClientTimesOut(t *testing.T) {
	release := make(chan struct{})
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Search(ctx, "slow", 10); err == nil {
		t.Fatal("expected a timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("a hung server must not hang the client")
	}
}

func TestAuditReadsPageVerdicts(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v1/skills/audit/o/r/mixed":
			fmt.Fprint(w, `{"audits":[
				{"provider":"Gen Agent Trust Hub","status":"pass","summary":"fine"},
				{"provider":"Snyk","status":"WARN","summary":"Risk: MEDIUM \u00b7 1 issue"},
				{"provider":"\u001b[31mEvil","status":"fail","summary":"bad \u001b[2J thing"},
				{"provider":"","status":"fail"},
				{"provider":"New Scanner","status":"pending"}
			]}`)
		case "/api/v1/skills/audit/o/r/unaudited":
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(500)
		}
	})
	a, err := c.Audit(context.Background(), "o/r", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.String(); got != "Gen Agent Trust Hub pass, Snyk warn (Risk: MEDIUM \u00b7 1 issue), [31mEvil fail (bad [2J thing), New Scanner pending" {
		t.Errorf("String() = %q", got)
	}
	if a.Worst() != Fail || !AtLeast(a.Worst(), Blocking) {
		t.Errorf("a fail verdict must block, worst = %q", a.Worst())
	}
	if a, err := c.Audit(context.Background(), "o/r", "unaudited"); err != nil || len(a) != 0 {
		t.Errorf("an unaudited skill must be empty, not an error: %v, %v", a, err)
	}
	if _, err := c.Audit(context.Background(), "../x", "a"); err == nil {
		t.Error("an invalid source must be rejected before any request")
	}
	if _, err := c.Audit(context.Background(), "o/r", "a,b"); err == nil {
		t.Error("an invalid skill must be rejected before any request")
	}
}

func TestAuditFindingsAreBounded(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		var rows []string
		for i := 0; i < 100; i++ {
			rows = append(rows, fmt.Sprintf(`{"provider":"P%d","status":"pass","summary":%q}`, i, strings.Repeat("x", 5000)))
		}
		fmt.Fprintf(w, `{"audits":[%s]}`, strings.Join(rows, ","))
	})
	a, err := c.Audit(context.Background(), "o/r", "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != maxFindings || len([]rune(a[0].Summary)) != maxSummary {
		t.Fatalf("got %d findings, summary %d runes", len(a), len([]rune(a[0].Summary)))
	}
}

// If the page endpoint starts requiring a token, audits fall back to the risk
// endpoint, mapped strictly so a critical risk still blocks.
func TestAuditFallsBackWhenPageEndpointLocks(t *testing.T) {
	var pageHits atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			pageHits.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/audit" || r.URL.Query().Get("source") != "o/r" {
			t.Errorf("unexpected request %s", r.URL)
		}
		fmt.Fprint(w, `{"s":{"ath":{"risk":"safe"},"socket":{"risk":"CRITICAL"},"snyk":{"risk":"medium"},"other":{"risk":"high"}}}`)
	})
	for i := 0; i < 3; i++ {
		a, err := c.Audit(context.Background(), "o/r", "s")
		if err != nil {
			t.Fatal(err)
		}
		if got := a.String(); got != "Gen Agent Trust Hub pass, Socket fail (risk: critical), Snyk warn (risk: medium)" {
			t.Fatalf("String() = %q", got)
		}
		if !AtLeast(a.Worst(), Blocking) {
			t.Fatal("a critical risk must block")
		}
	}
	if pageHits.Load() != 1 {
		t.Errorf("after one refusal the page endpoint should not be retried, hit %d times", pageHits.Load())
	}
}

func TestAuditSurfacesOtherErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
	if _, err := c.Audit(context.Background(), "o/r", "s"); err == nil {
		t.Fatal("a server error must be reported, not read as an empty audit")
	}
}

func TestRiskStatus(t *testing.T) {
	for risk, want := range map[string]string{"safe": Pass, "low": Pass, "medium": Warn, "high": Fail, "critical": Fail, "weird": Warn, "": Warn} {
		if got := riskStatus(risk); got != want {
			t.Errorf("riskStatus(%q) = %q, want %q", risk, got, want)
		}
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		status, min string
		want        bool
	}{
		{Pass, Blocking, false}, {Warn, Blocking, false}, {Fail, Blocking, true},
		{"pending", Blocking, false}, {"pending", Warn, true},
	}
	for _, c := range cases {
		if got := AtLeast(c.status, c.min); got != c.want {
			t.Errorf("AtLeast(%q, %q) = %v", c.status, c.min, got)
		}
	}
	if w := (Audit{{"A", Pass, ""}, {"B", "weird", ""}, {"C", Pass, ""}}).Worst(); w != "weird" {
		t.Errorf("an unknown verdict should rank as warn, above pass; got %q", w)
	}
}

func TestSkillMD(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/download/o/r/s":
			fmt.Fprint(w, `{"files":[{"path":"ref.md","contents":"no"},{"path":"skill.md","contents":"---\nname: s\n---"}]}`)
		case "/api/download/o/r/gone":
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected path %s", r.URL.EscapedPath())
			w.WriteHeader(500)
		}
	})
	md, err := c.SkillMD(context.Background(), "o/r", "s")
	if err != nil || !strings.Contains(md, "name: s") {
		t.Fatalf("SkillMD = %q, %v", md, err)
	}
	if md, err := c.SkillMD(context.Background(), "o/r", "gone"); err != nil || md != "" {
		t.Fatalf("a missing snapshot must be empty, not an error: %q, %v", md, err)
	}
	if _, err := c.SkillMD(context.Background(), "o/r", "../../x"); err == nil {
		t.Fatal("an invalid skill must be rejected")
	}
}

func TestPaceSpacesRequests(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"skills":[]}`) })
	c.Pace = 150 * time.Millisecond
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.Search(context.Background(), "query", 5); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 300*time.Millisecond {
		t.Fatalf("3 paced requests took %v, want >= 300ms", el)
	}
}

func TestRetryRateLimited(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"skills":[{"source":"a/b","skillId":"c","name":"c","installs":1}]}`)
	})
	if _, err := c.Search(context.Background(), "query", 5); err == nil {
		t.Fatal("without RetryRateLimited a 429 must surface")
	}
	calls.Store(0)
	c.RetryRateLimited = true
	got, err := c.Search(context.Background(), "query", 5)
	if err != nil || len(got) != 1 || calls.Load() != 2 {
		t.Fatalf("retry: got %v, %v after %d calls", got, err, calls.Load())
	}

	// A persistent 429 fails after one retry rather than looping.
	calls.Store(0)
	c2 := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c2.RetryRateLimited = true
	_, err = c2.Search(context.Background(), "query", 5)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 429 || calls.Load() != 2 {
		t.Fatalf("persistent 429: err %v after %d calls", err, calls.Load())
	}
}

func FuzzParseSpec(f *testing.F) {
	for _, s := range []string{"a/b/c", "https://skills.sh/a/b/c", "../b/c", "a/b/..", "uizze.sh/x", "a/b/c@v1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		src, sk, ok := ParseSpec(in)
		if !ok {
			return
		}
		if !ValidSource(src) || !ValidSlug(sk) {
			t.Fatalf("ParseSpec(%q) accepted invalid (%q, %q)", in, src, sk)
		}
		if strings.Contains(sk, "/") || sk == "." || sk == ".." || strings.HasPrefix(sk, "-") || strings.HasPrefix(sk, ".") {
			t.Fatalf("unsafe slug %q from %q", sk, in)
		}
		owner, repo, _ := strings.Cut(src, "/")
		if strings.HasPrefix(owner, "-") || strings.HasPrefix(repo, "-") || strings.Contains(src, "..") && (repo == ".." || owner == "..") {
			t.Fatalf("unsafe source %q from %q", src, in)
		}
	})
}

func FuzzClean(f *testing.F) {
	for _, s := range []string{"\x1b[31mx", "a\u202eb", "plain", "\u009b1;2H"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := Clean(in)
		for _, r := range out {
			if unicode.IsControl(r) || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
				t.Fatalf("Clean(%q) left control rune %U", in, r)
			}
		}
		if Clean(out) != out {
			t.Fatalf("Clean is not idempotent on %q", in)
		}
	})
}

// A decode error can quote server-chosen map keys; it must not carry escapes.
func TestDecodeErrorsAreCleaned(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			w.WriteHeader(http.StatusForbidden) // force the map-shaped risk endpoint
			return
		}
		fmt.Fprint(w, `{"x\u001b]0;PWNED\u0007\u001b[2J":{"ath":{"risk":1}}}`)
	})
	_, err := c.Audit(context.Background(), "o/r", "x")
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Fatalf("escape characters in error: %q", err)
	}
}
