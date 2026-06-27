package cli

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Brattlof/skillet/internal/install"
	"github.com/Brattlof/skillet/internal/registry"
)

func TestInstallFromLockSkipsMalicious(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "skillet.lock")
	t.Setenv("SKILLET_LOCKFILE", lock)
	// A traversal name and a file:// repo must both be rejected, not installed.
	if err := install.WriteLock(lock, install.Lockfile{Skills: []install.LockEntry{
		{Name: "../evil", Kind: "skill", Repo: "file:///etc", Path: "p", Commit: "deadbeef"},
	}}); err != nil {
		t.Fatal(err)
	}
	skdir := t.TempDir()
	captureStdout(t, func() { Run(context.Background(), []string{"install", "--dir", skdir}) })
	if _, err := os.Stat(filepath.Join(filepath.Dir(skdir), "evil")); !os.IsNotExist(err) {
		t.Fatal("a traversal lockfile entry must not create files outside the skills dir")
	}
}

func TestSplitNameRef(t *testing.T) {
	cases := []struct{ in, name, ref string }{
		{"hello", "hello", ""},
		{"hello@v1.2.3", "hello", "v1.2.3"},
		{"hello@abc123", "hello", "abc123"},
		{"@handle", "@handle", ""}, // a leading @ is not a ref split
	}
	for _, c := range cases {
		n, r := splitNameRef(c.in)
		if n != c.name || r != c.ref {
			t.Errorf("splitNameRef(%q) = (%q, %q), want (%q, %q)", c.in, n, r, c.name, c.ref)
		}
	}
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestInfoCommand(t *testing.T) {
	t.Setenv("SKILLET_OFFLINE", "1")
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())
	t.Setenv("SKILLET_SKILLS_DIR", t.TempDir()) // empty -> Installed: no

	var code int
	out := captureStdout(t, func() {
		code = Run(context.Background(), []string{"info", "hello-skill"})
	})
	if code != 0 {
		t.Fatalf("info exit = %d, want 0", code)
	}
	for _, want := range []string{"hello-skill", "examples/hello-skill", "Author", "Installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}
}

func TestInfoUnknownExitsNonZero(t *testing.T) {
	t.Setenv("SKILLET_OFFLINE", "1")
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())
	if code := Run(context.Background(), []string{"info", "nope-not-real"}); code == 0 {
		t.Fatal("expected non-zero exit for an unknown skill")
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		var code int
		out := captureStdout(t, func() { code = Run(context.Background(), []string{"completion", sh}) })
		if code != 0 {
			t.Fatalf("completion %s exit = %d", sh, code)
		}
		if !strings.Contains(out, "skillet") {
			t.Errorf("completion %s output looks empty:\n%s", sh, out)
		}
	}
	if code := Run(context.Background(), []string{"completion", "tcsh"}); code == 0 {
		t.Fatal("expected non-zero exit for an unsupported shell")
	}
}

func TestCompleteListsNames(t *testing.T) {
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir()) // empty cache -> embedded baseline
	out := captureStdout(t, func() { Run(context.Background(), []string{"__complete", "add"}) })
	if !strings.Contains(out, "hello-skill") {
		t.Errorf("__complete add should list hello-skill, got:\n%s", out)
	}
}

func TestCompleteListsRecordlessSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	skill := filepath.Join(home, ".claude", "skills", "manual-skill")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { Run(context.Background(), []string{"__complete", "remove"}) })
	if !strings.Contains(out, "manual-skill") {
		t.Errorf("__complete remove should list a hand-placed skill, got:\n%s", out)
	}
}

func TestListDeduplicatesCommandArtifacts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SKILLET_OFFLINE", "1")
	t.Setenv("SKILLET_CACHE_DIR", filepath.Join(home, "cache"))

	src := makeGitRepo(t, map[string]string{"commands/foo.md": "# foo\n"})
	target := filepath.Join(home, ".claude", "commands")
	e := registry.Entry{Name: "foo", Description: "d", Author: "t", Repo: src, Path: "commands/foo.md", Kind: "command"}
	if _, err := install.Install(context.Background(), e, target); err != nil {
		t.Fatalf("install: %v", err)
	}

	out := captureStdout(t, func() { Run(context.Background(), []string{"list"}) })
	if !strings.Contains(out, "foo") {
		t.Fatalf("list should show the command:\n%s", out)
	}
	if strings.Contains(out, "foo.md") {
		t.Fatalf("list must not also show the bare filename:\n%s", out)
	}
}

func TestListStatus(t *testing.T) {
	cases := []struct {
		name       string
		hasRec     bool
		inReg      bool
		rec        install.Record
		entry      registry.Entry
		cksumStale bool
		want       string
	}{
		{"no record", false, true, install.Record{}, registry.Entry{}, false, "no record"},
		{"not in registry", true, false, install.Record{}, registry.Entry{}, false, "not in registry"},
		{"registry added a pin", true, true, install.Record{Ref: ""}, registry.Entry{Ref: "v2"}, false, "update available"},
		{"pinned and matching", true, true, install.Record{Ref: "v1"}, registry.Entry{Ref: "v1"}, false, "up to date"},
		{"user pinned, registry unpinned", true, true, install.Record{Ref: "abc123"}, registry.Entry{}, false, "pinned"},
		{"cksum stale", true, true, install.Record{Cksum: "sha256.v2:a"}, registry.Entry{Cksum: "sha256:b"}, true, "update available"},
		{"unpinned tracking", true, true, install.Record{}, registry.Entry{}, false, "tracking"},
		{"cksum pinned and matching", true, true, install.Record{Cksum: "sha256.v2:a"}, registry.Entry{Cksum: "sha256:a"}, false, "up to date"},
	}
	for _, tc := range cases {
		if got := listStatus(tc.hasRec, tc.inReg, tc.rec, tc.entry, tc.cksumStale); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// list must compare the registry's published checksum against the installed
// artifact in the pin's own format, so a matching pin is not reported as an
// update. Cross-format matching (a v1 pin against a v2 record) is exercised in
// the install package's VerifyChecksum tests, which this path delegates to.
func TestListReportsRegistryRepin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SKILLET_CACHE_DIR", filepath.Join(home, "cache"))

	src := makeGitRepo(t, map[string]string{"commands/foo.md": "# foo\n"})
	target := filepath.Join(home, ".claude", "commands")
	base := registry.Entry{Name: "foo", Description: "d", Author: "t", Repo: src, Path: "commands/foo.md", Kind: "command"}
	if _, err := install.Install(context.Background(), base, target); err != nil {
		t.Fatalf("install: %v", err)
	}
	rec, ok, err := install.ReadRecord(target, "foo")
	if err != nil || !ok {
		t.Fatalf("read record ok=%v err=%v", ok, err)
	}

	serveIndex := func(cksum string) {
		// The registry entry is matched to the install by name; its repo only has
		// to pass index validation, so it need not be the local clone path.
		e := registry.Entry{
			Name: "foo", Description: "d", Author: "t",
			Repo: "https://example.com/foo", Path: "commands/foo.md",
			Kind: "command", Cksum: cksum,
		}
		idx, merr := json.Marshal([]registry.Entry{e})
		if merr != nil {
			t.Fatal(merr)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(idx)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("SKILLET_REGISTRY_URL", srv.URL)
		t.Setenv("SKILLET_CACHE_DIR", t.TempDir()) // force a fresh fetch each call
	}

	statusOf := func() string {
		out := captureStdout(t, func() { Run(context.Background(), []string{"list"}) })
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "foo") {
				return line
			}
		}
		t.Fatalf("list output has no foo row:\n%s", out)
		return ""
	}

	dest := filepath.Join(target, "foo.md")

	// A pin matching the installed content reads up to date even when the pin is
	// in the legacy v1 format and the record is v2. This is the case that fails if
	// the status uses a raw cross-format string compare instead of recomputing the
	// artifact in the pin's format.
	v1pin, err := install.HashArtifactAs(dest, "sha256:") // legacy format
	if err != nil {
		t.Fatal(err)
	}
	if v1pin == rec.Cksum {
		t.Fatal("test setup: the v1 pin must differ from the v2 record string")
	}
	serveIndex(v1pin)
	if line := statusOf(); strings.Contains(line, "update available") {
		t.Errorf("a v1 pin matching the content must read up to date: %q", line)
	}

	// A matching v2 pin reads up to date too.
	serveIndex(rec.Cksum)
	if line := statusOf(); strings.Contains(line, "update available") {
		t.Errorf("a matching v2 pin must read up to date: %q", line)
	}

	// A different pin (a genuine repin) is reported as an update.
	serveIndex("sha256:deadbeef")
	if line := statusOf(); !strings.Contains(line, "update available") {
		t.Errorf("a repinned cksum must report an update: %q", line)
	}
}

// parseArgs must accept flags before, after, or interspersed with positionals.
func TestParseArgsFlagPositions(t *testing.T) {
	cases := [][]string{
		{"--dir", "/tmp/x", "hello"}, // flag first
		{"hello", "--dir", "/tmp/x"}, // flag after positional (the bug)
		{"hello", "--dir=/tmp/x"},    // = form after positional
	}
	for _, args := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		dir := fs.String("dir", "", "")
		pos, err := parseArgs(fs, args)
		if err != nil {
			t.Fatalf("args %v: %v", args, err)
		}
		if len(pos) != 1 || pos[0] != "hello" {
			t.Fatalf("args %v: positionals = %v, want [hello]", args, pos)
		}
		if *dir != "/tmp/x" {
			t.Fatalf("args %v: dir = %q, want /tmp/x", args, *dir)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},         // shorter than limit, unchanged
		{"hello", 5, "hello"},          // exactly the limit, unchanged
		{"hello world", 8, "hello..."}, // truncated: 5 runes + ellipsis
		{"hello", 3, "hel"},            // n <= 3, no room for ellipsis
		{"hello", 1, "h"},              // small n must not panic
		{"hello", 0, ""},               // zero n must not panic
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
