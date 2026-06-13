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

func TestUpdate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	src := t.TempDir()
	skillDir := filepath.Join(src, "examples", "hello-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("# hello v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = src
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("-c", "user.email=t@t", "-c", "user.name=t", "add", "-A")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "v1")
	sha1 := git("rev-parse", "HEAD")

	dir := t.TempDir()
	e := registry.Entry{Name: "hello-skill", Description: "d", Author: "t", Repo: src, Path: "examples/hello-skill"}

	// first update installs the current HEAD
	prev, cur, err := Update(context.Background(), e, dir)
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	if prev.Commit != "" {
		t.Fatalf("expected no previous record, got %q", prev.Commit)
	}
	if cur.Commit != sha1 {
		t.Fatalf("expected commit %s, got %s", sha1, cur.Commit)
	}

	// move the upstream forward
	if err := os.WriteFile(skillFile, []byte("# hello v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qam", "v2")
	sha2 := git("rev-parse", "HEAD")

	prev, cur, err = Update(context.Background(), e, dir)
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	if prev.Commit != sha1 || cur.Commit != sha2 {
		t.Fatalf("expected %s -> %s, got %s -> %s", sha1, sha2, prev.Commit, cur.Commit)
	}

	// updating again with no upstream change is a no-op
	prev, cur, err = Update(context.Background(), e, dir)
	if err != nil {
		t.Fatalf("third update: %v", err)
	}
	if prev.Commit != cur.Commit || cur.Commit != sha2 {
		t.Fatalf("expected unchanged at %s, got %s -> %s", sha2, prev.Commit, cur.Commit)
	}
}

func TestDiagnose(t *testing.T) {
	dir := t.TempDir()
	mkSkill := func(name, body string) string {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}

	// healthy: dir + SKILL.md + record whose cksum matches the content
	hp := mkSkill("healthy", "# ok\n")
	sum, err := hashTree(hp)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(dir, Record{Name: "healthy", Repo: "https://x/y", Path: "p", Cksum: sum}); err != nil {
		t.Fatal(err)
	}
	// drift: record cksum no longer matches
	mkSkill("drifted", "# changed\n")
	if err := writeRecord(dir, Record{Name: "drifted", Repo: "https://x/y", Path: "p", Cksum: "sha256:stale"}); err != nil {
		t.Fatal(err)
	}
	// missing SKILL.md: dir + record, no SKILL.md
	mkSkill("nomd", "")
	if err := writeRecord(dir, Record{Name: "nomd", Repo: "https://x/y", Path: "p"}); err != nil {
		t.Fatal(err)
	}
	// broken: record but no install dir
	if err := writeRecord(dir, Record{Name: "broken", Repo: "https://x/y", Path: "p"}); err != nil {
		t.Fatal(err)
	}
	// orphan: dir + SKILL.md, no record
	mkSkill("orphan", "# orphan\n")

	diags, err := Diagnose(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Status{}
	for _, d := range diags {
		got[d.Name] = d.Status
	}
	want := map[string]Status{
		"healthy": StatusOK,
		"drifted": StatusDrift,
		"nomd":    StatusMissingSkillMD,
		"broken":  StatusBroken,
		"orphan":  StatusNoRecord,
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: got %q, want %q", n, got[n], w)
		}
	}
}

func TestLockRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "skillet.lock")

	if lf, err := ReadLock(p); err != nil || len(lf.Skills) != 0 {
		t.Fatalf("missing lock should be empty: %+v err=%v", lf, err)
	}

	var lf Lockfile
	lf.Upsert(LockEntry{Name: "b", Repo: "https://x/b", Path: "p", Commit: "2"})
	lf.Upsert(LockEntry{Name: "a", Repo: "https://x/a", Path: "p", Commit: "1"})
	lf.Upsert(LockEntry{Name: "A", Repo: "https://x/a2", Path: "p", Commit: "1b"}) // replaces "a"
	if len(lf.Skills) != 2 {
		t.Fatalf("upsert should dedupe case-insensitively: %+v", lf.Skills)
	}

	if err := WriteLock(p, lf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 2 || got.Skills[0].Name != "A" || got.Skills[1].Name != "b" {
		t.Fatalf("expected sorted [A b], got %+v", got.Skills)
	}
	if got.Skills[0].Commit != "1b" {
		t.Fatalf("expected replaced commit 1b, got %q", got.Skills[0].Commit)
	}
}

func TestTargetDirRouting(t *testing.T) {
	t.Setenv("SKILLET_SKILLS_DIR", "") // force default skills path

	// an explicit override wins for every kind
	for _, k := range []string{"skill", "command", "hook"} {
		got, err := TargetDir(k, "/tmp/x")
		if err != nil {
			t.Fatal(err)
		}
		if got != "/tmp/x" {
			t.Fatalf("override for %q = %q", k, got)
		}
	}

	want := map[string]string{
		"skill":   filepath.Join(".claude", "skills"),
		"command": filepath.Join(".claude", "commands"),
		"hook":    filepath.Join(".claude", "hooks"),
	}
	for kind, suffix := range want {
		got, err := TargetDir(kind, "")
		if err != nil {
			t.Fatalf("TargetDir(%q): %v", kind, err)
		}
		if !strings.HasSuffix(got, suffix) {
			t.Errorf("TargetDir(%q) = %q, want suffix %q", kind, got, suffix)
		}
	}

	if _, err := TargetDir("bogus", ""); err == nil {
		t.Fatal("expected an unknown kind to error")
	}
}

func TestRejectsTraversal(t *testing.T) {
	if _, _, err := FindInstall("../x", t.TempDir()); err == nil {
		t.Error("FindInstall should reject a traversal name")
	}
	// Install rejects unsafe name/path up front, before touching git or the disk.
	if _, err := Install(context.Background(), registry.Entry{Name: "../x", Repo: "https://x/y", Path: "p"}, t.TempDir()); err == nil {
		t.Error("Install should reject a traversal name")
	}
	if _, err := Install(context.Background(), registry.Entry{Name: "ok", Repo: "https://x/y", Path: "../escape"}, t.TempDir()); err == nil {
		t.Error("Install should reject a traversal path")
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
