// Package registry loads and queries the skill index.
//
// The source of truth is one JSON shard per skill under skills/. CI compiles the
// shards into a single index published on the gh-pages branch. At runtime Load
// prefers a fresh local cache, revalidates with the remote index, and falls back
// to the cache and finally to the shards embedded in the binary, so it works offline.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	skillet "github.com/Brattlof/skillet"
)

// Entry is a single curated skill in the registry.
type Entry struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Repo        string    `json:"repo"`
	Path        string    `json:"path"`
	Author      string    `json:"author"`
	Tags        []string  `json:"tags"`
	Kind        string    `json:"kind,omitempty"`  // skill (default), command, hook, or mcp
	Ref         string    `json:"ref,omitempty"`   // commit SHA or tag to pin the install to
	Cksum       string    `json:"cksum,omitempty"` // sha256[.v2]: artifact hash, verified on install
	Hook        *HookSpec `json:"hook,omitempty"`  // required for kind hook: how to register it
	MCP         *MCPSpec  `json:"mcp,omitempty"`   // required for kind mcp: the server to register
}

// HookSpec is how a hook registers in ~/.claude/settings.json. The installed
// script is added under the named event; an empty matcher matches everything.
type HookSpec struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher,omitempty"`
}

// MCPSpec describes an MCP server to register in a tool's MCP config. A stdio
// server sets Command (with optional Args and Env); a remote server sets URL
// instead. Exactly one of Command or URL is set.
type MCPSpec struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
}

// KindOrDefault returns the entry's kind, defaulting to "skill".
func (e Entry) KindOrDefault() string {
	if e.Kind == "" {
		return "skill"
	}
	return e.Kind
}

// hookEvents are the Claude Code hook events a hook entry may register for.
var hookEvents = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "PostToolBatch": true,
	"UserPromptSubmit": true, "UserPromptExpansion": true,
	"SessionStart": true, "SessionEnd": true,
	"Stop": true, "StopFailure": true, "SubagentStop": true,
	"Notification": true, "FileChanged": true, "PermissionRequest": true,
	"WorktreeCreate": true, "Elicitation": true, "PreCompact": true,
}

// defaultRegistryURL serves the compiled index straight from the gh-pages
// branch. raw.githubusercontent.com reflects a push within a few minutes,
// where a CDN in front of the branch can lag a registry update by hours.
// Override with SKILLET_REGISTRY_URL.
const defaultRegistryURL = "https://raw.githubusercontent.com/Brattlof/skillet/gh-pages/index.json"

// cacheTTL is how long a cached index is trusted before revalidating.
const cacheTTL = time.Hour

var httpClient = &http.Client{Timeout: 10 * time.Second}

// Load returns the skill index. It prefers a fresh on-disk cache, then revalidates
// with the remote index, and falls back to the cache and finally to the embedded
// baseline. It does not fail for a transient network problem as long as the
// embedded shards parse.
func Load(ctx context.Context) ([]Entry, error) {
	if offline() {
		if entries, _, ok := readCache(); ok {
			return entries, nil
		}
		return loadEmbedded()
	}

	cached, meta, haveCache := readCache()
	if haveCache && time.Since(meta.Fetched) < cacheTTL {
		return cached, nil
	}

	body, etag, status, err := fetchIndex(ctx, meta.ETag)
	if err == nil && status == http.StatusOK {
		if entries, derr := decodeIndex(body); derr == nil {
			writeCache(body, etag)
			return entries, nil
		}
	}
	if err == nil && status == http.StatusNotModified && haveCache {
		touchCache(meta.ETag)
		return cached, nil
	}
	if haveCache {
		return cached, nil
	}
	return loadEmbedded()
}

// Cached returns the index from the local cache, or the embedded baseline if no
// cache exists. It never touches the network, so it is safe for latency-sensitive
// paths like shell completion.
func Cached() ([]Entry, error) {
	if entries, _, ok := readCache(); ok {
		return entries, nil
	}
	return loadEmbedded()
}

// Find returns the entry whose name matches (case-insensitively).
func Find(ctx context.Context, name string) (Entry, bool, error) {
	entries, err := Load(ctx)
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// Search returns entries matching query, ranked by relevance: an exact or prefix
// name match beats a name substring, which beats a tag, which beats the
// description, which beats a fuzzy (subsequence) name match. An empty query
// returns every entry sorted by name.
func Search(ctx context.Context, query string) ([]Entry, error) {
	entries, err := Load(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return entries, nil
	}

	type scored struct {
		e Entry
		s int
	}
	var hits []scored
	for _, e := range entries {
		if s := scoreEntry(e, q); s > 0 {
			hits = append(hits, scored{e, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s > hits[j].s
		}
		return hits[i].e.Name < hits[j].e.Name
	})

	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out, nil
}

// scoreEntry rates an entry against a lowercased query. Higher is more relevant;
// zero means no match.
func scoreEntry(e Entry, q string) int {
	name := strings.ToLower(e.Name)
	score := 0
	upd := func(s int) {
		if s > score {
			score = s
		}
	}
	switch {
	case name == q:
		upd(100)
	case strings.HasPrefix(name, q):
		upd(80)
	case strings.Contains(name, q):
		upd(60)
	}
	for _, t := range e.Tags {
		tl := strings.ToLower(t)
		if tl == q {
			upd(50)
		} else if strings.Contains(tl, q) {
			upd(30)
		}
	}
	if strings.Contains(strings.ToLower(e.Description), q) {
		upd(40)
	}
	if score == 0 && fuzzyMatch(name, q) {
		upd(20)
	}
	return score
}

// fuzzyMatch reports whether the runes of q appear in order within s.
func fuzzyMatch(s, q string) bool {
	qr := []rune(q)
	i := 0
	for _, c := range s {
		if i < len(qr) && qr[i] == c {
			i++
		}
	}
	return i == len(qr)
}

// BuildIndex reads every *.json shard under root's per-kind directories (skills,
// commands, hooks), validates and de-duplicates them, and returns the sorted
// index. Used by the cmd/buildindex compiler.
func BuildIndex(root string) ([]Entry, error) {
	return parseShards(os.DirFS(root))
}

// ValidateInstall checks the fields skillet needs to install an entry safely. For
// a repo-backed kind (everything but mcp) that means a safe single-component name,
// an http(s) repo, a contained path, and a valid kind, ref, and cksum. An mcp
// entry is a server spec rather than a repo, so it is validated separately. It does not
// require descriptive metadata, so it also validates an untrusted lockfile entry
// before it reaches git or the filesystem.
func ValidateInstall(e Entry) error {
	if !validName(e.Name) {
		return fmt.Errorf("invalid name %q (no separators or path traversal)", e.Name)
	}
	if e.KindOrDefault() == "mcp" {
		return validateMCP(e)
	}
	switch {
	case strings.TrimSpace(e.Repo) == "":
		return errors.New("missing repo")
	case !strings.HasPrefix(e.Repo, "http://") && !strings.HasPrefix(e.Repo, "https://"):
		return errors.New("repo must be an http(s) URL")
	case strings.TrimSpace(e.Path) == "":
		return errors.New("missing path")
	}
	// A skill can be the whole repository, with SKILL.md at the root and the
	// path set to ".". IsLocal already permits ".", but it denotes a directory,
	// which only fits a skill; the single-file kinds must point at a file.
	if e.Path == "." {
		if e.KindOrDefault() != "skill" {
			return fmt.Errorf("path %q (the repo root) is only valid for a skill", e.Path)
		}
	} else if !filepath.IsLocal(filepath.FromSlash(e.Path)) {
		return fmt.Errorf("invalid path %q (must stay within the repo)", e.Path)
	}
	if e.Kind != "" && e.Kind != "skill" && e.Kind != "command" && e.Kind != "hook" &&
		e.Kind != "agent" && e.Kind != "output-style" {
		return fmt.Errorf("invalid kind %q (want skill, command, hook, agent, output-style, or mcp)", e.Kind)
	}
	if e.Kind == "hook" {
		if e.Hook == nil || strings.TrimSpace(e.Hook.Event) == "" {
			return errors.New("a hook entry requires hook.event")
		}
		if !hookEvents[e.Hook.Event] {
			return fmt.Errorf("invalid hook event %q", e.Hook.Event)
		}
	} else if e.Hook != nil {
		return fmt.Errorf("hook spec is only valid for kind hook, not %q", e.KindOrDefault())
	}
	if e.Ref != "" && !validRef(e.Ref) {
		return fmt.Errorf("invalid ref %q", e.Ref)
	}
	if e.Cksum != "" && !strings.HasPrefix(e.Cksum, "sha256:") && !strings.HasPrefix(e.Cksum, "sha256.v2:") {
		return fmt.Errorf("invalid cksum %q (want sha256:... or sha256.v2:...)", e.Cksum)
	}
	return nil
}

// validateMCP checks an mcp entry: it carries a server spec with exactly one of a
// stdio command or a remote http(s) url, and no fields meant for file installs.
func validateMCP(e Entry) error {
	if e.MCP == nil {
		return errors.New("an mcp entry requires an mcp spec")
	}
	hasCmd := strings.TrimSpace(e.MCP.Command) != ""
	hasURL := strings.TrimSpace(e.MCP.URL) != ""
	if hasCmd == hasURL {
		return errors.New("an mcp entry needs exactly one of command (stdio) or url (remote)")
	}
	if hasURL && !strings.HasPrefix(e.MCP.URL, "http://") && !strings.HasPrefix(e.MCP.URL, "https://") {
		return fmt.Errorf("mcp url must be an http(s) URL, got %q", e.MCP.URL)
	}
	if e.Hook != nil {
		return errors.New("hook spec is only valid for kind hook")
	}
	return nil
}

// Validate is the full registry-entry check: descriptive metadata plus everything
// ValidateInstall covers. Registry shards and the compiled index use this.
func Validate(e Entry) error {
	switch {
	case strings.TrimSpace(e.Description) == "":
		return errors.New("missing description")
	case strings.TrimSpace(e.Author) == "":
		return errors.New("missing author")
	}
	return ValidateInstall(e)
}

// validName requires a single safe path component: no separators and no traversal,
// so the name can be used as a directory and filename without escaping its parent.
func validName(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && filepath.IsLocal(s)
}

// validRef allows only plain git ref characters and forbids a leading dash, so a
// ref can never be parsed by git as an option.
func validRef(s string) bool {
	if strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '/' || r == '-':
		default:
			return false
		}
	}
	return s != ""
}

func loadEmbedded() ([]Entry, error) {
	return parseShards(skillet.SkillsFS)
}

// kindDirs maps each artifact kind to its top-level shard directory. The kind is
// inferred from the directory, so a shard file does not repeat it.
var kindDirs = []struct{ kind, dir string }{
	{"skill", "skills"},
	{"command", "commands"},
	{"hook", "hooks"},
	{"mcp", "mcp"},
	{"agent", "agents"},
	{"output-style", "output-styles"},
}

// parseShards reads every *.json shard from the per-kind directories of fsys
// (skills, commands, hooks), tagging each entry with the kind of its directory.
// Names are unique across all kinds, every shard must live in its first-letter
// subdirectory (commands/c/changelog.json), and the result is sorted by name. A
// kind directory that does not exist is skipped.
func parseShards(fsys fs.FS) ([]Entry, error) {
	seen := make(map[string]bool)
	var out []Entry
	for _, kd := range kindDirs {
		es, err := parseKindShards(fsys, kd.dir, kd.kind, seen)
		if err != nil {
			return nil, err
		}
		out = append(out, es...)
	}
	sortEntries(out)
	return out, nil
}

func parseKindShards(fsys fs.FS, dir, kind string, seen map[string]bool) ([]Entry, error) {
	if _, err := fs.Stat(fsys, dir); err != nil {
		return nil, nil // no shards of this kind
	}
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".json") {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]Entry, 0, len(names))
	for _, n := range names {
		b, err := fs.ReadFile(sub, n)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", dir, n, err)
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", dir, n, err)
		}
		if e.Kind != "" && e.Kind != kind {
			return nil, fmt.Errorf("%s/%s: kind %q does not match the %s/ directory", dir, n, e.Kind, dir)
		}
		// The directory is the source of truth for the kind. Skills keep the
		// empty default so the compiled index stays free of redundant fields.
		if kind != "skill" {
			e.Kind = kind
		}
		if err := Validate(e); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", dir, n, err)
		}
		if want := shardDir(e.Name); path.Dir(n) != want {
			return nil, fmt.Errorf("%s/%s: %q must live in %s/%s/%s.json", dir, n, e.Name, dir, want, e.Name)
		}
		key := strings.ToLower(e.Name)
		if seen[key] {
			return nil, fmt.Errorf("%s/%s: duplicate name %q", dir, n, e.Name)
		}
		seen[key] = true
		out = append(out, e)
	}
	return out, nil
}

// shardDir returns the shard subdirectory an entry belongs in: the lowercased
// first character of its name (for example "git-commit" -> "g").
func shardDir(name string) string {
	if name == "" {
		return "."
	}
	return strings.ToLower(name[:1])
}

// decodeIndex parses the compiled index (a JSON array of entries) and validates
// each one, since the index arrives from the network or an on-disk cache and is
// therefore untrusted. A bad entry rejects the whole index so Load falls back.
func decodeIndex(b []byte) ([]Entry, error) {
	var entries []Entry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("parsing index: %w", err)
	}
	for i := range entries {
		if err := Validate(entries[i]); err != nil {
			return nil, fmt.Errorf("index entry %q: %w", entries[i].Name, err)
		}
	}
	sortEntries(entries)
	return entries, nil
}

func sortEntries(e []Entry) {
	sort.Slice(e, func(i, j int) bool { return e[i].Name < e[j].Name })
}

func indexURL() string {
	if v := os.Getenv("SKILLET_REGISTRY_URL"); v != "" {
		return v
	}
	return defaultRegistryURL
}

func offline() bool {
	switch strings.ToLower(os.Getenv("SKILLET_OFFLINE")) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func fetchIndex(ctx context.Context, etag string) (body []byte, newETag string, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL(), nil)
	if err != nil {
		return nil, "", 0, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", resp.StatusCode, fmt.Errorf("registry returned %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", resp.StatusCode, err
	}
	return b, resp.Header.Get("ETag"), resp.StatusCode, nil
}

type cacheMeta struct {
	ETag    string    `json:"etag"`
	Fetched time.Time `json:"fetched"`
}

func cacheDir() string {
	if v := os.Getenv("SKILLET_CACHE_DIR"); v != "" {
		return v
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "skillet")
}

func readCache() ([]Entry, cacheMeta, bool) {
	dir := cacheDir()
	if dir == "" {
		return nil, cacheMeta{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, cacheMeta{}, false
	}
	entries, err := decodeIndex(b)
	if err != nil {
		return nil, cacheMeta{}, false
	}
	var m cacheMeta
	if mb, err := os.ReadFile(filepath.Join(dir, "index.meta")); err == nil {
		_ = json.Unmarshal(mb, &m)
	}
	return entries, m, true
}

func writeCache(body []byte, etag string) {
	dir := cacheDir()
	if dir == "" || os.MkdirAll(dir, 0o755) != nil {
		return
	}
	if err := writeFileAtomic(filepath.Join(dir, "index.json"), body); err != nil {
		return // do not stamp meta for an index we failed to persist
	}
	touchCache(etag)
}

func touchCache(etag string) {
	dir := cacheDir()
	if dir == "" {
		return
	}
	if mb, err := json.Marshal(cacheMeta{ETag: etag, Fetched: time.Now()}); err == nil {
		_ = writeFileAtomic(filepath.Join(dir, "index.meta"), mb)
	}
}

// writeFileAtomic writes b to path via a temp file and rename, so a crash or a
// concurrent process never leaves a half-written file.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
