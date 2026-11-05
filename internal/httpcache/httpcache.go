// Package httpcache fetches small JSON documents with an on-disk cache: a fresh
// cache is used as-is, a stale one is revalidated with its ETag, and any network
// failure falls back to whatever cache exists. Every body is passed through the
// caller's validate func before it is returned or stored, because both the
// network and the cache directory are untrusted.
package httpcache

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxBody bounds a fetched document.
const maxBody = 64 << 20

// Dir is the cache directory: $SKILLET_CACHE_DIR, or skillet under the user
// cache dir. It is "" if neither is available, which disables caching.
func Dir() string {
	if v := os.Getenv("SKILLET_CACHE_DIR"); v != "" {
		return v
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "skillet")
}

// Offline reports whether $SKILLET_OFFLINE asks skillet to stay off the network.
func Offline() bool {
	switch strings.ToLower(os.Getenv("SKILLET_OFFLINE")) {
	case "1", "true", "yes":
		return true
	}
	return false
}

type meta struct {
	ETag    string    `json:"etag"`
	Fetched time.Time `json:"fetched"`
}

// Get returns the document at url, cached under name. ok is false only when
// there is neither a valid fetch nor a valid cache, so the caller can fall back.
// Offline mode never touches the network. The returned body always passed
// validate; decode it, rather than keeping state from inside validate, which
// also sees bodies that are then rejected.
func Get(ctx context.Context, client *http.Client, url, name string, ttl time.Duration, validate func([]byte) error) (body []byte, ok bool) {
	cached, m, haveCache := read(name, validate)
	if Offline() {
		return cached, haveCache
	}
	if haveCache && time.Since(m.Fetched) < ttl {
		return cached, true
	}

	b, etag, status, err := fetch(ctx, client, url, m.ETag)
	if err == nil && status == http.StatusOK && validate(b) == nil {
		write(name, b, etag)
		return b, true
	}
	if err == nil && status == http.StatusNotModified && haveCache {
		touch(name, m.ETag)
		return cached, true
	}
	return cached, haveCache
}

// Cached returns the cached document under name if it exists and validates. It
// never touches the network.
func Cached(name string, validate func([]byte) error) ([]byte, bool) {
	b, _, ok := read(name, validate)
	return b, ok
}

func read(name string, validate func([]byte) error) ([]byte, meta, bool) {
	dir := Dir()
	if dir == "" {
		return nil, meta{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || validate(b) != nil {
		return nil, meta{}, false
	}
	var m meta
	if mb, err := os.ReadFile(filepath.Join(dir, metaName(name))); err == nil {
		_ = json.Unmarshal(mb, &m)
	}
	return b, m, true
}

// Fetched reports when the document under name was last fetched or
// revalidated, or the zero time if unknown.
func Fetched(name string) time.Time {
	dir := Dir()
	if dir == "" {
		return time.Time{}
	}
	var m meta
	if mb, err := os.ReadFile(filepath.Join(dir, metaName(name))); err == nil {
		_ = json.Unmarshal(mb, &m)
	}
	return m.Fetched
}

// metaName keeps the existing "index.meta" name for "index.json".
func metaName(name string) string { return strings.TrimSuffix(name, ".json") + ".meta" }

func fetch(ctx context.Context, client *http.Client, url, etag string) (body []byte, newETag string, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", 0, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", resp.StatusCode, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, "", resp.StatusCode, err
	}
	return b, resp.Header.Get("ETag"), resp.StatusCode, nil
}

func write(name string, body []byte, etag string) {
	dir := Dir()
	if dir == "" || os.MkdirAll(dir, 0o755) != nil {
		return
	}
	if err := WriteFileAtomic(filepath.Join(dir, name), body); err != nil {
		return // do not stamp meta for a document we failed to persist
	}
	touch(name, etag)
}

func touch(name, etag string) {
	dir := Dir()
	if dir == "" {
		return
	}
	if mb, err := json.Marshal(meta{ETag: etag, Fetched: time.Now()}); err == nil {
		_ = WriteFileAtomic(filepath.Join(dir, metaName(name)), mb)
	}
}

// WriteFileAtomic writes b to path via a temp file and rename, so a crash or a
// concurrent process never leaves a half-written file.
func WriteFileAtomic(path string, b []byte) error {
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
