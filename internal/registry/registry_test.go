package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func offlineEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SKILLET_OFFLINE", "1")
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())
}

func TestLoadEmbedded(t *testing.T) {
	offlineEnv(t)
	entries, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded index is empty; expected at least hello-skill")
	}
}

func TestFind(t *testing.T) {
	offlineEnv(t)
	e, ok, err := Find(context.Background(), "hello-skill")
	if err != nil || !ok {
		t.Fatalf("expected to find hello-skill, ok=%v err=%v", ok, err)
	}
	if e.Repo == "" || e.Path == "" {
		t.Fatalf("hello-skill missing repo/path: %+v", e)
	}
	if _, ok, _ := Find(context.Background(), "does-not-exist"); ok {
		t.Fatal("did not expect to find a bogus skill")
	}
}

func TestSearch(t *testing.T) {
	offlineEnv(t)
	res, err := Search(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("expected a search hit for 'example'")
	}
	all, err := Search(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < len(res) {
		t.Fatal("empty query should return all entries")
	}
}

func writeShard(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIndexSortsAndValidates(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "b.json", `{"name":"beta","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	writeShard(t, dir, "a.json", `{"name":"alpha","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	entries, err := BuildIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "alpha" || entries[1].Name != "beta" {
		t.Fatalf("expected sorted [alpha beta], got %+v", entries)
	}
}

func TestBuildIndexRejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "a.json", `{"name":"Dup","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	writeShard(t, dir, "b.json", `{"name":"dup","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	if _, err := BuildIndex(dir); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestBuildIndexRejectsMissingField(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "a.json", `{"name":"x","repo":"https://x/y","path":"p","author":"a"}`) // no description
	if _, err := BuildIndex(dir); err == nil {
		t.Fatal("expected validation error for missing description")
	}
}

func TestValidateRejectsUnsafeRefAndCksum(t *testing.T) {
	base := Entry{Name: "x", Description: "d", Repo: "https://x/y", Path: "p", Author: "a"}
	good := base
	good.Ref = "v1.2.3"
	if err := Validate(good); err != nil {
		t.Fatalf("expected v1.2.3 to be a valid ref: %v", err)
	}
	for _, bad := range []string{"--orphan", "-x", "a b", "foo;rm -rf"} {
		e := base
		e.Ref = bad
		if err := Validate(e); err == nil {
			t.Fatalf("expected ref %q to be rejected", bad)
		}
	}
	bc := base
	bc.Cksum = "notasha"
	if err := Validate(bc); err == nil {
		t.Fatal("expected non-sha256 cksum to be rejected")
	}
}

func TestKindValidationAndDefault(t *testing.T) {
	base := Entry{Name: "x", Description: "d", Repo: "https://x/y", Path: "p", Author: "a"}
	if base.KindOrDefault() != "skill" {
		t.Fatalf("default kind = %q, want skill", base.KindOrDefault())
	}
	for _, k := range []string{"skill", "command", "hook"} {
		e := base
		e.Kind = k
		if err := Validate(e); err != nil {
			t.Fatalf("kind %q should be valid: %v", k, err)
		}
	}
	bad := base
	bad.Kind = "plugin"
	if err := Validate(bad); err == nil {
		t.Fatal("expected an invalid kind to be rejected")
	}
}

func TestLoadFetchesRemoteIndex(t *testing.T) {
	idx := `[{"name":"remote-skill","description":"d","repo":"https://x/y","path":"p","author":"a"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(idx))
	}))
	defer srv.Close()

	t.Setenv("SKILLET_REGISTRY_URL", srv.URL)
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())

	entries, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "remote-skill" {
		t.Fatalf("expected remote-skill, got %+v", entries)
	}
}

func TestLoadRejectsInvalidRemoteAndFallsBack(t *testing.T) {
	// Remote serves an entry missing a description; the whole index is rejected
	// and Load falls back to the embedded baseline rather than trusting it.
	idx := `[{"name":"evil","repo":"https://x/y","path":"p","author":"a"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(idx))
	}))
	defer srv.Close()

	t.Setenv("SKILLET_REGISTRY_URL", srv.URL)
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())

	entries, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "evil" {
			t.Fatal("invalid remote entry must not reach the caller")
		}
	}
	if len(entries) == 0 {
		t.Fatal("expected embedded fallback to return at least hello-skill")
	}
}

func TestLoadFallsBackToEmbedded(t *testing.T) {
	// Unconnectable URL forces a fetch error; with an empty cache, Load must
	// fall back to the embedded baseline rather than error out.
	t.Setenv("SKILLET_REGISTRY_URL", "http://127.0.0.1:0/index.json")
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())
	entries, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected embedded fallback to return at least hello-skill")
	}
}
