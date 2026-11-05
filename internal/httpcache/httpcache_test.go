package httpcache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func validJSON(b []byte) error {
	if len(b) == 0 || b[0] != '{' {
		return errors.New("not an object")
	}
	return nil
}

type server struct {
	hits atomic.Int32
	body atomic.Value // string
	code atomic.Int32
}

func newServer(t *testing.T, body string) (*server, string) {
	t.Helper()
	s := &server{}
	s.body.Store(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if c := s.code.Load(); c != 0 {
			w.WriteHeader(int(c))
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(s.body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func TestGetCachesAndRevalidates(t *testing.T) {
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())
	s, url := newServer(t, `{"v":1}`)
	ctx := context.Background()

	b, ok := Get(ctx, http.DefaultClient, url, "doc.json", time.Hour, validJSON)
	if !ok || string(b) != `{"v":1}` || s.hits.Load() != 1 {
		t.Fatalf("first get: %q %v hits=%d", b, ok, s.hits.Load())
	}
	// Fresh cache: no request.
	if b, ok := Get(ctx, http.DefaultClient, url, "doc.json", time.Hour, validJSON); !ok || string(b) != `{"v":1}` || s.hits.Load() != 1 {
		t.Fatalf("fresh cache should not hit the network: hits=%d", s.hits.Load())
	}
	// Stale cache: revalidated with the ETag, 304 keeps the cached body.
	if b, ok := Get(ctx, http.DefaultClient, url, "doc.json", 0, validJSON); !ok || string(b) != `{"v":1}` || s.hits.Load() != 2 {
		t.Fatalf("revalidate: %q %v hits=%d", b, ok, s.hits.Load())
	}
	if Fetched("doc.json").IsZero() {
		t.Fatal("fetch time not recorded")
	}
}

func TestGetRejectsInvalidAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKILLET_CACHE_DIR", dir)
	s, url := newServer(t, `{"good":true}`)
	ctx := context.Background()
	if _, ok := Get(ctx, http.DefaultClient, url, "doc.json", 0, validJSON); !ok {
		t.Fatal("seed failed")
	}

	// The server now serves garbage: the cached copy wins and is not overwritten.
	s.body.Store(`garbage`)
	_ = os.Remove(filepath.Join(dir, "doc.meta")) // drop the ETag so the server sends a body
	b, ok := Get(ctx, http.DefaultClient, url, "doc.json", 0, validJSON)
	if !ok || string(b) != `{"good":true}` {
		t.Fatalf("invalid remote must fall back to cache, got %q %v", b, ok)
	}
	if on, _ := os.ReadFile(filepath.Join(dir, "doc.json")); string(on) != `{"good":true}` {
		t.Fatalf("invalid body was cached: %q", on)
	}

	// Server errors also fall back.
	s.code.Store(500)
	if b, ok := Get(ctx, http.DefaultClient, url, "doc.json", 0, validJSON); !ok || string(b) != `{"good":true}` {
		t.Fatalf("server error must fall back to cache, got %q %v", b, ok)
	}

	// A tampered cache file is ignored.
	if err := os.WriteFile(filepath.Join(dir, "doc.json"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, ok := Cached("doc.json", validJSON); ok {
		t.Fatalf("an invalid cache must not be returned: %q", b)
	}
	if _, ok := Get(ctx, http.DefaultClient, url, "doc.json", time.Hour, validJSON); ok {
		t.Fatal("no valid source left; Get must report not ok")
	}
}

func TestGetOffline(t *testing.T) {
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())
	s, url := newServer(t, `{"v":1}`)
	t.Setenv("SKILLET_OFFLINE", "1")
	if _, ok := Get(context.Background(), http.DefaultClient, url, "doc.json", 0, validJSON); ok {
		t.Fatal("offline with no cache must report not ok")
	}
	if s.hits.Load() != 0 {
		t.Fatal("offline mode touched the network")
	}
}
