// Package registry loads and queries the skill index.
//
// The source of truth is one JSON shard per skill under skills/. CI compiles the
// shards into a single index published over a CDN. At runtime Load prefers a fresh
// local cache, revalidates with the remote index, and falls back to the cache and
// finally to the shards embedded in the binary, so it works offline.
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
	"path/filepath"
	"sort"
	"strings"
	"time"

	skillet "github.com/Brattlof/skillet"
)

// Entry is a single curated skill in the registry.
type Entry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Repo        string   `json:"repo"`
	Path        string   `json:"path"`
	Author      string   `json:"author"`
	Tags        []string `json:"tags"`
	Kind        string   `json:"kind,omitempty"`  // skill (default), command, or hook
	Ref         string   `json:"ref,omitempty"`   // commit SHA or tag to pin the install to
	Cksum       string   `json:"cksum,omitempty"` // sha256: tree hash, verified on install
}

// KindOrDefault returns the entry's kind, defaulting to "skill".
func (e Entry) KindOrDefault() string {
	if e.Kind == "" {
		return "skill"
	}
	return e.Kind
}

// defaultRegistryURL serves the compiled index over a free CDN (jsDelivr fronting
// the gh-pages branch). Override with SKILLET_REGISTRY_URL.
const defaultRegistryURL = "https://cdn.jsdelivr.net/gh/Brattlof/skillet@gh-pages/index.json"

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

// BuildIndex reads every *.json shard in dir, validates and de-duplicates them,
// and returns the sorted index. Used by the cmd/buildindex compiler.
func BuildIndex(dir string) ([]Entry, error) {
	return parseShards(os.DirFS(dir))
}

// ValidateInstall checks the fields skillet needs to install a skill safely: a
// safe single-component name, an http(s) repo, a contained path, and a valid
// kind, ref, and cksum. It does not require descriptive metadata, so it also
// validates an untrusted lockfile entry before it reaches git or the filesystem.
func ValidateInstall(e Entry) error {
	if !validName(e.Name) {
		return fmt.Errorf("invalid name %q (no separators or path traversal)", e.Name)
	}
	switch {
	case strings.TrimSpace(e.Repo) == "":
		return errors.New("missing repo")
	case !strings.HasPrefix(e.Repo, "http://") && !strings.HasPrefix(e.Repo, "https://"):
		return errors.New("repo must be an http(s) URL")
	case strings.TrimSpace(e.Path) == "":
		return errors.New("missing path")
	}
	if !filepath.IsLocal(filepath.FromSlash(e.Path)) {
		return fmt.Errorf("invalid path %q (must stay within the repo)", e.Path)
	}
	if e.Kind != "" && e.Kind != "skill" && e.Kind != "command" && e.Kind != "hook" {
		return fmt.Errorf("invalid kind %q (want skill, command, or hook)", e.Kind)
	}
	if e.Ref != "" && !validRef(e.Ref) {
		return fmt.Errorf("invalid ref %q", e.Ref)
	}
	if e.Cksum != "" && !strings.HasPrefix(e.Cksum, "sha256:") {
		return fmt.Errorf("invalid cksum %q (want sha256:...)", e.Cksum)
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
	sub, err := fs.Sub(skillet.SkillsFS, "skills")
	if err != nil {
		return nil, err
	}
	return parseShards(sub)
}

// parseShards reads each *.json file (one Entry per file) from fsys, validates it,
// rejects duplicate names, and returns the entries sorted by name.
func parseShards(fsys fs.FS) ([]Entry, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	seen := make(map[string]bool, len(names))
	out := make([]Entry, 0, len(names))
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if err := Validate(e); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		key := strings.ToLower(e.Name)
		if seen[key] {
			return nil, fmt.Errorf("%s: duplicate skill name %q", n, e.Name)
		}
		seen[key] = true
		out = append(out, e)
	}
	sortEntries(out)
	return out, nil
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
