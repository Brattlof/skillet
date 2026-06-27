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
	full := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIndexSortsAndValidates(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "skills/b/beta.json", `{"name":"beta","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	writeShard(t, dir, "skills/a/alpha.json", `{"name":"alpha","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
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
	writeShard(t, dir, "skills/d/Dup.json", `{"name":"Dup","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	writeShard(t, dir, "skills/d/dup.json", `{"name":"dup","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	if _, err := BuildIndex(dir); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestBuildIndexRejectsMissingField(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "skills/x/x.json", `{"name":"x","repo":"https://x/y","path":"p","author":"a"}`) // no description
	if _, err := BuildIndex(dir); err == nil {
		t.Fatal("expected validation error for missing description")
	}
}

func TestBuildIndexRejectsMisplacedShard(t *testing.T) {
	dir := t.TempDir()
	// "alpha" belongs in a/, not z/
	writeShard(t, dir, "skills/z/alpha.json", `{"name":"alpha","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	if _, err := BuildIndex(dir); err == nil {
		t.Fatal("expected a placement error for a misfiled shard")
	}
}

func TestBuildIndexInfersKindFromDirectory(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "skills/s/sk.json", `{"name":"sk","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	writeShard(t, dir, "commands/c/cmd.json", `{"name":"cmd","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	writeShard(t, dir, "hooks/h/hk.json", `{"name":"hk","description":"d","repo":"https://x/y","path":"p","author":"a","hook":{"event":"SessionStart"}}`)

	entries, err := BuildIndex(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := map[string]string{"sk": "skill", "cmd": "command", "hk": "hook"}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Name] = e.KindOrDefault()
	}
	for name, k := range want {
		if got[name] != k {
			t.Errorf("%s: kind = %q, want %q", name, got[name], k)
		}
	}
}

func TestBuildIndexRejectsHookWithoutEvent(t *testing.T) {
	dir := t.TempDir()
	writeShard(t, dir, "hooks/b/bad.json", `{"name":"bad","description":"d","repo":"https://x/y","path":"p","author":"a"}`)
	if _, err := BuildIndex(dir); err == nil {
		t.Fatal("a hook shard with no hook.event should be rejected")
	}
}

func TestBuildIndexRejectsKindFolderMismatch(t *testing.T) {
	dir := t.TempDir()
	// A shard that declares a kind contradicting its directory is an error.
	writeShard(t, dir, "commands/h/x.json", `{"name":"x","description":"d","repo":"https://x/y","path":"p","author":"a","kind":"hook"}`)
	if _, err := BuildIndex(dir); err == nil {
		t.Fatal("a kind/folder mismatch should be rejected")
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

func TestValidateInstall(t *testing.T) {
	// lock-shaped entry: no description/author, but safe to install
	ok := Entry{Name: "x", Repo: "https://x/y", Path: "p"}
	if err := ValidateInstall(ok); err != nil {
		t.Fatalf("valid install entry rejected: %v", err)
	}
	// the full Validate still requires descriptive metadata
	if err := Validate(ok); err == nil {
		t.Fatal("full Validate should require description and author")
	}

	bad := []Entry{
		{Name: "x", Repo: "file:///etc", Path: "p"},                       // non-http transport
		{Name: "x", Repo: "git@h:r", Path: "p"},                           // ssh transport
		{Name: "x", Repo: "http://x/y", Path: "p"},                        // plaintext http repo
		{Name: "../x", Repo: "https://x/y", Path: "p"},                    // traversal name
		{Name: "x", Repo: "https://x/y", Path: "../e"},                    // traversal path
		{Name: "x", Path: "p"},                                            // missing repo
		{Name: "x", Repo: "https://x/y", Path: "p", Ref: "--upload-pack"}, // option-shaped ref
	}
	for i, e := range bad {
		if err := ValidateInstall(e); err == nil {
			t.Errorf("bad install entry %d (%+v) should be rejected", i, e)
		}
	}
}

func TestValidateRootLevelSkill(t *testing.T) {
	// A skill whose SKILL.md is at the repo root carries the path ".".
	root := Entry{Name: "x", Description: "d", Repo: "https://x/y", Path: ".", Author: "a"}
	if err := Validate(root); err != nil {
		t.Fatalf("root-level skill rejected: %v", err)
	}
	if err := ValidateInstall(Entry{Name: "x", Repo: "https://x/y", Path: "."}); err != nil {
		t.Fatalf("root-level skill (lock-shaped) rejected: %v", err)
	}
	// "." only makes sense for a skill; the single-file kinds must point at a file.
	for _, k := range []string{"command", "hook", "agent", "output-style"} {
		e := root
		e.Kind = k
		if err := ValidateInstall(e); err == nil {
			t.Errorf("path %q should be rejected for kind %q", e.Path, k)
		}
	}
}

func TestValidateRejectsTraversal(t *testing.T) {
	base := Entry{Name: "ok", Description: "d", Repo: "https://x/y", Path: "p", Author: "a"}
	if err := Validate(base); err != nil {
		t.Fatalf("clean entry rejected: %v", err)
	}
	for _, n := range []string{"../evil", "a/b", "..", "/abs", `a\b`} {
		e := base
		e.Name = n
		if err := Validate(e); err == nil {
			t.Errorf("name %q should be rejected", n)
		}
	}
	for _, p := range []string{"../../etc", "/etc", ".."} {
		e := base
		e.Path = p
		if err := Validate(e); err == nil {
			t.Errorf("path %q should be rejected", p)
		}
	}
}

func TestKindValidationAndDefault(t *testing.T) {
	base := Entry{Name: "x", Description: "d", Repo: "https://x/y", Path: "p", Author: "a"}
	if base.KindOrDefault() != "skill" {
		t.Fatalf("default kind = %q, want skill", base.KindOrDefault())
	}
	for _, k := range []string{"skill", "command", "agent", "output-style"} {
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

	// A hook needs a valid event; without one it is rejected.
	hook := base
	hook.Kind = "hook"
	if err := Validate(hook); err == nil {
		t.Fatal("a hook with no event should be rejected")
	}
	hook.Hook = &HookSpec{Event: "PreToolUse", Matcher: "Bash"}
	if err := Validate(hook); err != nil {
		t.Fatalf("a valid hook should pass: %v", err)
	}
	hook.Hook = &HookSpec{Event: "NotAnEvent"}
	if err := Validate(hook); err == nil {
		t.Fatal("an unknown hook event should be rejected")
	}

	// A hook spec on a non-hook kind is rejected.
	misplaced := base
	misplaced.Kind = "command"
	misplaced.Hook = &HookSpec{Event: "PreToolUse"}
	if err := Validate(misplaced); err == nil {
		t.Fatal("a hook spec on a command should be rejected")
	}
}

func TestValidateMCP(t *testing.T) {
	base := Entry{Name: "m", Description: "d", Author: "a", Kind: "mcp"}
	if err := Validate(base); err == nil {
		t.Fatal("an mcp entry with no spec should be rejected")
	}

	stdio := base
	stdio.MCP = &MCPSpec{Command: "npx", Args: []string{"-y", "pkg"}}
	if err := Validate(stdio); err != nil {
		t.Fatalf("valid stdio mcp should pass: %v", err)
	}

	remote := base
	remote.MCP = &MCPSpec{URL: "https://x/y"}
	if err := Validate(remote); err != nil {
		t.Fatalf("valid remote mcp should pass: %v", err)
	}

	// Plaintext http is allowed only for a loopback host (local MCP dev).
	for _, raw := range []string{
		"http://localhost:3000/mcp",
		"http://LocalHost:3000/mcp", // host matching is case-insensitive
		"http://127.0.0.1:3000/mcp",
		"http://[::1]:3000/mcp",
	} {
		local := base
		local.MCP = &MCPSpec{URL: raw}
		if err := Validate(local); err != nil {
			t.Fatalf("loopback http mcp %q should pass: %v", raw, err)
		}
	}

	for name, spec := range map[string]*MCPSpec{
		"both command and url": {Command: "x", URL: "https://x/y"},
		"neither":              {},
		"non-http url":         {URL: "ftp://x/y"},
		"plaintext http url":   {URL: "http://example.com/mcp"},
	} {
		bad := base
		bad.MCP = spec
		if err := Validate(bad); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}

	// An mcp entry must not require repo/path (it is a server spec, not a repo).
	if base.Repo != "" || base.Path != "" {
		t.Fatal("test setup: mcp base should have no repo/path")
	}
}

func TestFuzzyMatch(t *testing.T) {
	if !fuzzyMatch("hello-skill", "hsk") {
		t.Error("hsk should fuzzy-match hello-skill")
	}
	if !fuzzyMatch("hello", "") {
		t.Error("empty query should match")
	}
	if fuzzyMatch("hello", "xyz") {
		t.Error("xyz should not match hello")
	}
}

func TestScoreEntryOrdering(t *testing.T) {
	nameExact := scoreEntry(Entry{Name: "abc"}, "abc")
	namePrefix := scoreEntry(Entry{Name: "abcdef"}, "abc")
	tagExact := scoreEntry(Entry{Name: "x", Tags: []string{"abc"}}, "abc")
	descHit := scoreEntry(Entry{Name: "x", Description: "abc"}, "abc")
	none := scoreEntry(Entry{Name: "x", Description: "y"}, "abc")

	if !(nameExact > namePrefix && namePrefix > tagExact && tagExact > descHit) {
		t.Fatalf("bad ordering: exact=%d prefix=%d tag=%d desc=%d", nameExact, namePrefix, tagExact, descHit)
	}
	if none != 0 {
		t.Fatalf("no match should score 0, got %d", none)
	}
}

func TestSearchRanksResults(t *testing.T) {
	dir := t.TempDir()
	idx := `[
	  {"name":"json","description":"d","repo":"https://x/1","path":"p","author":"a","tags":["fmt"]},
	  {"name":"yaml","description":"convert to json","repo":"https://x/2","path":"p","author":"a","tags":["json"]},
	  {"name":"zzz","description":"d","repo":"https://x/3","path":"p","author":"a","tags":["jsonish"]}
	]`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.meta"), []byte(`{"etag":"","fetched":"2030-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKILLET_OFFLINE", "1")
	t.Setenv("SKILLET_CACHE_DIR", dir)

	res, err := Search(context.Background(), "json")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("expected 3 hits, got %d", len(res))
	}
	if res[0].Name != "json" || res[2].Name != "zzz" {
		t.Fatalf("expected json first and zzz last, got %s ... %s", res[0].Name, res[2].Name)
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
