// Package skillssh is a small client for skills.sh, Vercel's public directory of
// Agent Skills. skillet uses it for community search, security audits, and the
// trending and hot leaderboards.
//
// Everything skills.sh returns is untrusted: identifiers are validated before they
// reach a URL, git, or the filesystem, and text is stripped of control characters
// before it reaches the terminal. The endpoints used here are the ones the
// official `npx skills` CLI calls; they need no account.
package skillssh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// SourceName marks an install that came from skills.sh in records and lockfiles.
const SourceName = "skills.sh"

// DefaultBaseURL is the skills.sh origin. The apex domain redirects here.
const DefaultBaseURL = "https://www.skills.sh"

// UserAgent identifies skillet to skills.sh, so its operators can see and reach
// the traffic.
const UserAgent = "skillet (+https://github.com/Brattlof/skillet)"

// Response size caps. A search page is ~30 KB and a leaderboard page under 1 MB,
// so these leave headroom while bounding a hostile or broken server.
const (
	maxJSONBody = 4 << 20
	maxHTMLBody = 8 << 20
)

// Client talks to skills.sh.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Pace, when set, is the minimum gap between requests. The CLI makes one or
	// two requests per command and leaves it zero; the snapshot builder makes
	// hundreds and paces itself under the skills.sh per-IP rate limit.
	Pace time.Duration
	// RetryRateLimited retries a 429 once after its Retry-After (up to two
	// minutes). Only the builder sets it; a user should not wait silently.
	RetryRateLimited bool

	mu          sync.Mutex
	last        time.Time
	legacyAudit atomic.Bool // the page audit endpoint refused; use the risk endpoint
}

// New returns a client for $SKILLET_SKILLSSH_URL, or skills.sh itself.
func New() *Client {
	base := os.Getenv("SKILLET_SKILLSSH_URL")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Skill is one skills.sh listing.
type Skill struct {
	Source   string `json:"source"`  // owner/repo on GitHub
	SkillID  string `json:"skillId"` // slug of the SKILL.md name
	Name     string `json:"name"`
	Installs int64  `json:"installs"`
	Change   int64  `json:"change,omitempty"` // hot view: this hour minus the same hour yesterday
	Official bool   `json:"isOfficial,omitempty"`
}

// ID is the skill's install spec: owner/repo/skill.
func (s Skill) ID() string { return s.Source + "/" + s.SkillID }

// RepoURL is the GitHub repository a skill installs from.
func RepoURL(source string) string { return "https://github.com/" + source }

// PageURL is the skill's page on skills.sh.
func PageURL(source, skill string) string { return DefaultBaseURL + "/" + source + "/" + skill }

var (
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repoRe  = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,99}$`)
	slugRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

// ValidSource reports whether s is a GitHub owner/repo that is safe to put in a
// URL. It rejects skills.sh's non-GitHub "well-known" sources (bare domains),
// which skillet cannot clone, and anything with traversal or option-like parts.
func ValidSource(s string) bool {
	owner, repo, ok := strings.Cut(s, "/")
	if !ok || !ownerRe.MatchString(owner) || !repoRe.MatchString(repo) {
		return false
	}
	return repo != "." && repo != ".."
}

// ValidSlug reports whether s is a skill slug that is safe to use as a single
// directory name: lowercase, no separators, no leading dot or dash.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// Slug normalizes a skill name the way skills.sh derives a skillId from the
// SKILL.md name: lowercase, whitespace and underscores to dashes, everything
// else outside [a-z0-9-] dropped, dashes collapsed and trimmed.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// ParseSpec parses a community install spec: owner/repo/skill, optionally as a
// skills.sh URL. ok is false for anything else, including a plain registry name.
func ParseSpec(arg string) (source, skill string, ok bool) {
	s := arg
	for _, p := range []string{"https://www.skills.sh/", "https://skills.sh/", "www.skills.sh/", "skills.sh/"} {
		if strings.HasPrefix(strings.ToLower(s), p) {
			s = s[len(p):]
			break
		}
	}
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return "", "", false
	}
	source = parts[0] + "/" + parts[1]
	skill = strings.ToLower(parts[2])
	if !ValidSource(source) || !ValidSlug(skill) {
		return "", "", false
	}
	return source, skill, true
}

// unsafeRune reports runes that can change how a terminal renders text: C0 and
// C1 controls (ANSI escapes start with one), line separators, bidi overrides and
// isolates, and the replacement character that marks invalid UTF-8.
func unsafeRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '\u2028' || r == '\u2029' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') || r == '\ufffd'
}

// Printable reports whether s is safe to print as-is: no rune Clean would drop.
func Printable(s string) bool {
	for _, r := range s {
		if unsafeRune(r) {
			return false
		}
	}
	return true
}

// Clean strips control characters (including ANSI escape introducers) and
// collapses whitespace, so remote text cannot rewrite the user's terminal.
func Clean(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			space = b.Len() > 0
		case unsafeRune(r):
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Search queries skills.sh and returns up to limit valid GitHub-hosted skills.
// A query shorter than two characters returns nothing, matching the API.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Skill, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 {
		return nil, nil
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	v := url.Values{"q": {query}, "limit": {fmt.Sprint(limit)}}
	var resp struct {
		Skills []Skill `json:"skills"`
	}
	if err := c.getJSON(ctx, "/api/search?"+v.Encode(), &resp); err != nil {
		return nil, err
	}
	out := make([]Skill, 0, len(resp.Skills))
	for _, s := range resp.Skills {
		if s, ok := sanitize(s); ok {
			out = append(out, s)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// sanitize validates a listing's identifiers and cleans its text. Listings that
// skillet cannot install safely (a bad id, a non-GitHub source) are dropped.
func sanitize(s Skill) (Skill, bool) {
	if !ValidSource(s.Source) || !ValidSlug(s.SkillID) {
		return Skill{}, false
	}
	s.Name = Clean(s.Name)
	if s.Name == "" {
		s.Name = s.SkillID
	}
	if s.Installs < 0 {
		s.Installs = 0
	}
	return s, true
}

// Audit verdicts, as skills.sh shows them on a skill's page.
const (
	Pass = "pass"
	Warn = "warn"
	Fail = "fail"
)

var statusRank = map[string]int{Pass: 0, Warn: 1, Fail: 2}

// Blocking is the verdict that stops an install without --force.
const Blocking = Fail

// AtLeast reports whether status is at or above min. An unrecognized status
// counts as warn: worth showing, not worth blocking on.
func AtLeast(status, min string) bool {
	r, ok := statusRank[status]
	if !ok {
		r = statusRank[Warn]
	}
	return r >= statusRank[min]
}

// Finding is one audit partner's verdict on a skill.
type Finding struct {
	Provider string
	Status   string // pass, warn, or fail
	Summary  string // the partner's one-line reason, e.g. "Risk: MEDIUM · 1 issue"
}

// Audit is every partner verdict for one skill, in the order skills.sh lists them.
type Audit []Finding

// Worst returns the most severe verdict in the audit, or "" if there are none.
func (a Audit) Worst() string {
	worst, rank := "", -1
	for _, f := range a {
		r, ok := statusRank[f.Status]
		if !ok {
			r = statusRank[Warn]
		}
		if r > rank {
			worst, rank = f.Status, r
		}
	}
	return worst
}

// String renders the audit on one line, with the reason for any warn or fail:
// "Socket pass, Snyk warn (Risk: MEDIUM · 1 issue)".
func (a Audit) String() string {
	parts := make([]string, len(a))
	for i, f := range a {
		parts[i] = f.Provider + " " + f.Status
		if f.Status != Pass && f.Summary != "" {
			parts[i] += " (" + shorten(f.Summary, 80) + ")"
		}
	}
	return strings.Join(parts, ", ")
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Bounds on an audit reply, which is untrusted.
const (
	maxFindings = 20
	maxSummary  = 300
)

// Audit fetches the security audit skills.sh shows on a skill's page. A skill
// skills.sh has not audited yet returns an empty Audit and no error.
//
// It reads the page's own endpoint, which skills.sh documents as needing a
// Vercel token but serves without one. If that changes (401 or 403), it falls
// back to the older per-risk endpoint the skills CLI uses, so installs keep
// their audit gate.
func (c *Client) Audit(ctx context.Context, source, skill string) (Audit, error) {
	if !ValidSource(source) || !ValidSlug(skill) {
		return nil, fmt.Errorf("invalid skill %s/%s", source, skill)
	}
	if !c.legacyAudit.Load() {
		a, err := c.pageAudit(ctx, source, skill)
		var se *StatusError
		if !errors.As(err, &se) || (se.Code != http.StatusUnauthorized && se.Code != http.StatusForbidden) {
			return a, err
		}
		c.legacyAudit.Store(true)
	}
	return c.riskAudit(ctx, source, skill)
}

func (c *Client) pageAudit(ctx context.Context, source, skill string) (Audit, error) {
	owner, repo, _ := strings.Cut(source, "/")
	p := "/api/v1/skills/audit/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/" + url.PathEscape(skill)
	var resp struct {
		Audits []struct {
			Provider string `json:"provider"`
			Status   string `json:"status"`
			Summary  string `json:"summary"`
		} `json:"audits"`
	}
	err := c.getJSON(ctx, p, &resp)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return nil, nil // not audited yet
	}
	if err != nil {
		return nil, err
	}
	var a Audit
	for _, f := range resp.Audits {
		if len(a) == maxFindings {
			break
		}
		name := shorten(Clean(f.Provider), 40)
		if name == "" {
			continue
		}
		a = append(a, Finding{
			Provider: name,
			Status:   strings.ToLower(shorten(Clean(f.Status), 16)),
			Summary:  shorten(Clean(f.Summary), maxSummary),
		})
	}
	return a, nil
}

// riskProviders are the partners in the older risk endpoint, in page order.
var riskProviders = []struct{ key, name string }{
	{"ath", "Gen Agent Trust Hub"},
	{"socket", "Socket"},
	{"snyk", "Snyk"},
}

// riskStatus maps the older endpoint's risk levels onto page verdicts. It is
// stricter than the page: skills.sh sometimes shows a "critical" risk as warn,
// and skillet would rather block than miss a real failure.
func riskStatus(risk string) string {
	switch risk {
	case "safe", "low":
		return Pass
	case "high", "critical":
		return Fail
	default:
		return Warn
	}
}

func (c *Client) riskAudit(ctx context.Context, source, skill string) (Audit, error) {
	v := url.Values{"source": {source}, "skills": {skill}}
	var raw map[string]map[string]struct {
		Risk string `json:"risk"`
	}
	if err := c.getJSON(ctx, "/api/audit?"+v.Encode(), &raw); err != nil {
		return nil, err
	}
	var a Audit
	for _, p := range riskProviders {
		if f, ok := raw[skill][p.key]; ok && f.Risk != "" {
			risk := strings.ToLower(shorten(Clean(f.Risk), 16))
			a = append(a, Finding{Provider: p.name, Status: riskStatus(risk), Summary: "risk: " + risk})
		}
	}
	return a, nil
}

// SkillMD fetches a skill's SKILL.md from the skills.sh snapshot. It returns ""
// with no error when skills.sh holds no snapshot.
func (c *Client) SkillMD(ctx context.Context, source, skill string) (string, error) {
	if !ValidSource(source) || !ValidSlug(skill) {
		return "", fmt.Errorf("invalid skill %s/%s", source, skill)
	}
	owner, repo, _ := strings.Cut(source, "/")
	p := "/api/download/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/" + url.PathEscape(skill)
	var resp struct {
		Files []struct {
			Path     string `json:"path"`
			Contents string `json:"contents"`
		} `json:"files"`
	}
	err := c.getJSON(ctx, p, &resp)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, f := range resp.Files {
		if strings.EqualFold(f.Path, "SKILL.md") {
			return f.Contents, nil
		}
	}
	return "", nil
}

// StatusError is a non-200 reply from skills.sh.
type StatusError struct {
	Code       int
	RetryAfter string
}

func (e *StatusError) Error() string {
	if e.Code == http.StatusTooManyRequests {
		msg := "skills.sh rate limit reached"
		if e.RetryAfter != "" {
			msg += "; retry in " + e.RetryAfter + "s"
		}
		return msg
	}
	return fmt.Sprintf("skills.sh returned HTTP %d", e.Code)
}

func (c *Client) get(ctx context.Context, path string, limit int64) ([]byte, error) {
	b, err := c.getOnce(ctx, path, limit)
	var se *StatusError
	if c.RetryRateLimited && errors.As(err, &se) && se.Code == http.StatusTooManyRequests {
		wait := 30 * time.Second
		if n, perr := strconv.Atoi(se.RetryAfter); perr == nil && n > 0 && n <= 120 {
			wait = time.Duration(n) * time.Second
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		return c.getOnce(ctx, path, limit)
	}
	return b, err
}

// wait blocks until Pace has passed since the previous request.
func (c *Client) wait(ctx context.Context) error {
	if c.Pace <= 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := time.Until(c.last.Add(c.Pace)); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	c.last = time.Now()
	return nil
}

func (c *Client) getOnce(ctx context.Context, path string, limit int64) ([]byte, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, &StatusError{Code: resp.StatusCode, RetryAfter: Clean(resp.Header.Get("Retry-After"))}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("skills.sh response exceeds %d bytes", limit)
	}
	return b, nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	b, err := c.get(ctx, path, maxJSONBody)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		// A decode error can quote server-chosen map keys verbatim; clean it.
		return fmt.Errorf("decoding skills.sh response: %s", Clean(err.Error()))
	}
	return nil
}
