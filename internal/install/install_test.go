package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Brattlof/skillet/internal/registry"
)

func TestSkillsDirPriority(t *testing.T) {
	// override wins
	got, err := SkillsDir("/tmp/explicit")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/explicit" {
		t.Fatalf("override: got %q", got)
	}

	// env wins over default
	t.Setenv("SKILLET_SKILLS_DIR", "/tmp/from-env")
	got, err = SkillsDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/from-env" {
		t.Fatalf("env: got %q", got)
	}
}

func TestListRemoveRoundTrip(t *testing.T) {
	dir := t.TempDir()

	names, err := ListInstalled(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty, got %v", names)
	}

	skill := filepath.Join(dir, "demo")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	names, _ = ListInstalled(dir)
	if len(names) != 1 || names[0] != "demo" {
		t.Fatalf("expected [demo], got %v", names)
	}

	// the .skillet metadata dir must not be listed as a skill
	if err := os.MkdirAll(filepath.Join(dir, ".skillet"), 0o755); err != nil {
		t.Fatal(err)
	}
	names, _ = ListInstalled(dir)
	if len(names) != 1 || names[0] != "demo" {
		t.Fatalf("expected [demo] (no .skillet), got %v", names)
	}

	if err := Remove("demo", dir); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	names, _ = ListInstalled(dir)
	if len(names) != 0 {
		t.Fatalf("expected empty after remove, got %v", names)
	}

	if err := Remove("demo", dir); err == nil {
		t.Fatal("removing a missing skill should error")
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if recs, err := Records(dir); err != nil || len(recs) != 0 {
		t.Fatalf("empty dir: recs=%v err=%v", recs, err)
	}

	r := Record{Name: "demo", Repo: "https://x/y", Path: "p", Commit: "abc", Cksum: "sha256:1"}
	if err := writeRecord(dir, r); err != nil {
		t.Fatal(err)
	}

	got, ok, err := ReadRecord(dir, "demo")
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if got.Repo != "https://x/y" || got.Commit != "abc" {
		t.Fatalf("record mismatch: %+v", got)
	}

	recs, err := Records(dir)
	if err != nil || len(recs) != 1 || recs[0].Name != "demo" {
		t.Fatalf("records: %v err=%v", recs, err)
	}

	if err := removeRecord(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadRecord(dir, "demo"); ok {
		t.Fatal("expected record removed")
	}
	if err := removeRecord(dir, "demo"); err != nil {
		t.Fatal("removing a missing record should be a no-op")
	}
}

func TestHashTreeDeterministicAndSensitive(t *testing.T) {
	mk := func() string {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, "a.txt"), []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(d, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}

	h1, err := hashTree(mk())
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hashTree(mk())
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %s vs %s", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Fatalf("unexpected hash format: %s", h1)
	}

	changed := mk()
	if err := os.WriteFile(filepath.Join(changed, "a.txt"), []byte("different"), 0o644); err != nil {
		t.Fatal(err)
	}
	h3, err := hashTree(changed)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h1 {
		t.Fatal("hash should change when content changes")
	}
}

// TestInstallPinnedAndChecksum exercises ref checkout and cksum verification
// against a throwaway local git repo.
func TestInstallPinnedAndChecksum(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	src := t.TempDir()
	skillDir := filepath.Join(src, "examples", "hello-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = src
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.email=t@t", "-c", "user.name=t", "add", "-A")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")

	base := registry.Entry{Name: "hello-skill", Description: "d", Author: "t", Repo: src, Path: "examples/hello-skill"}

	// plain install records provenance
	dir := t.TempDir()
	got, err := Install(context.Background(), base, dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	sum, err := hashTree(got)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := ReadRecord(dir, "hello-skill")
	if err != nil || !ok {
		t.Fatalf("expected a manifest record, ok=%v err=%v", ok, err)
	}
	if rec.Cksum != sum || rec.Commit == "" || rec.Repo != src {
		t.Fatalf("record mismatch: %+v (sum=%s)", rec, sum)
	}
	if err := Remove("hello-skill", dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok, _ := ReadRecord(dir, "hello-skill"); ok {
		t.Fatal("record not cleared on remove")
	}

	// correct checksum installs cleanly
	withCksum := base
	withCksum.Cksum = sum
	if _, err := Install(context.Background(), withCksum, t.TempDir()); err != nil {
		t.Fatalf("install with correct cksum: %v", err)
	}

	// wrong checksum is rejected and the partial install is removed
	bad := base
	bad.Cksum = "sha256:deadbeef"
	dst := t.TempDir()
	if _, err := Install(context.Background(), bad, dst); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, err := os.Stat(filepath.Join(dst, "hello-skill")); !os.IsNotExist(err) {
		t.Fatal("expected the failed install to be cleaned up")
	}

	// pinned ref (full SHA) takes the full-clone-and-checkout path
	shaCmd := exec.Command("git", "rev-parse", "HEAD")
	shaCmd.Dir = src
	shaOut, err := shaCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	pinned := base
	pinned.Ref = strings.TrimSpace(string(shaOut))
	if _, err := Install(context.Background(), pinned, t.TempDir()); err != nil {
		t.Fatalf("pinned-ref install: %v", err)
	}
}
