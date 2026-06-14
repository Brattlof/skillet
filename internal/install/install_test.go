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

	names, err := ListInstalled(dir, "skill")
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

	names, _ = ListInstalled(dir, "skill")
	if len(names) != 1 || names[0] != "demo" {
		t.Fatalf("expected [demo], got %v", names)
	}

	// the .skillet metadata dir must not be listed as a skill
	if err := os.MkdirAll(filepath.Join(dir, ".skillet"), 0o755); err != nil {
		t.Fatal(err)
	}
	names, _ = ListInstalled(dir, "skill")
	if len(names) != 1 || names[0] != "demo" {
		t.Fatalf("expected [demo] (no .skillet), got %v", names)
	}

	if err := Remove("demo", dir); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	names, _ = ListInstalled(dir, "skill")
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

	diags, err := Diagnose(dir, "skill")
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

	// an explicit override wins for every kind and target
	for _, k := range []string{"skill", "command", "hook"} {
		got, err := TargetDir(k, "claude", "/tmp/x")
		if err != nil {
			t.Fatal(err)
		}
		if got != "/tmp/x" {
			t.Fatalf("override for %q = %q", k, got)
		}
	}

	want := map[string]string{
		"skill":        filepath.Join(".claude", "skills"),
		"command":      filepath.Join(".claude", "commands"),
		"hook":         filepath.Join(".claude", "hooks"),
		"agent":        filepath.Join(".claude", "agents"),
		"output-style": filepath.Join(".claude", "output-styles"),
	}
	for kind, suffix := range want {
		got, err := TargetDir(kind, "claude", "")
		if err != nil {
			t.Fatalf("TargetDir(%q): %v", kind, err)
		}
		if !strings.HasSuffix(got, suffix) {
			t.Errorf("TargetDir(%q) = %q, want suffix %q", kind, got, suffix)
		}
	}

	if _, err := TargetDir("bogus", "claude", ""); err == nil {
		t.Fatal("expected an unknown kind to error")
	}

	// the agents target installs skills under ~/.agents/skills and rejects the
	// Claude-specific kinds.
	got, err := TargetDir("skill", "agents", "")
	if err != nil {
		t.Fatalf("agents skill: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join(".agents", "skills")) {
		t.Errorf("agents skill dir = %q, want suffix .agents/skills", got)
	}
	for _, k := range []string{"command", "hook", "agent", "output-style"} {
		if _, err := TargetDir(k, "agents", ""); err == nil {
			t.Errorf("agents target should reject kind %q", k)
		}
	}
	if _, err := TargetDir("skill", "bogus", ""); err == nil {
		t.Fatal("expected an unknown target to error")
	}
}

func TestScanDirsByTarget(t *testing.T) {
	claude, err := ScanDirs("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(claude) != 5 {
		t.Fatalf("claude should scan skills, commands, hooks, agents, output-styles; got %d dirs", len(claude))
	}
	agents, err := ScanDirs("agents", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Kind != "skill" {
		t.Fatalf("agents should scan skills only, got %+v", agents)
	}
	if _, err := ScanDirs("bogus", ""); err == nil {
		t.Fatal("an unknown target should error")
	}
}

func TestRejectsTraversal(t *testing.T) {
	if _, _, err := FindInstall("../x", "claude", t.TempDir()); err == nil {
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

// gitRepoWith builds a one-commit git repo containing files (keyed by slash path),
// for the command and hook install tests. Files ending in .sh are made executable.
func gitRepoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
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
	return src
}

func TestCopyDirSkipsGitAndSymlinks(t *testing.T) {
	src := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("SKILL.md", "# skill\n")
	write(".git/config", "[core]\n")           // version control: excluded
	write(".github/workflows/ci.yml", "on:\n") // not version control: kept
	write(".gitignore", "node_modules\n")      // not version control: kept

	// A symlink pointing at a secret outside the source tree must not be followed.
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(src, "leak")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	dst := t.TempDir()
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	kept := []string{"SKILL.md", ".github/workflows/ci.yml", ".gitignore"}
	for _, rel := range kept {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected %q to be copied: %v", rel, err)
		}
	}
	gone := []string{".git", ".git/config", "leak"}
	for _, rel := range gone {
		if _, err := os.Lstat(filepath.Join(dst, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%q should not be copied, lstat err = %v", rel, err)
		}
	}
}

func TestInstallRootLevelSkill(t *testing.T) {
	src := gitRepoWith(t, map[string]string{
		"SKILL.md":         "# root skill\n",
		"reference/api.md": "# api\n",
		"README.md":        "# repo\n",
	})
	dir := t.TempDir()
	e := registry.Entry{Name: "root-skill", Description: "d", Author: "t", Repo: src, Path: ".", Kind: "skill"}
	dest, err := Install(context.Background(), e, dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	// The whole repo becomes the skill directory, with SKILL.md at its root.
	for _, rel := range []string{"SKILL.md", "reference/api.md", "README.md"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected %q in the installed skill: %v", rel, err)
		}
	}
	// Version-control metadata never ships.
	if _, err := os.Stat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git should not be copied into the skill, stat err = %v", err)
	}
	rec, ok, err := ReadRecord(dir, "root-skill")
	if err != nil || !ok {
		t.Fatalf("expected a manifest record, ok=%v err=%v", ok, err)
	}
	if rec.Path != "." {
		t.Errorf("record path = %q, want %q", rec.Path, ".")
	}
}

func TestInstallCommand(t *testing.T) {
	src := gitRepoWith(t, map[string]string{"commands/foo.md": "# foo command\n"})
	dir := t.TempDir()
	e := registry.Entry{Name: "foo", Description: "d", Author: "t", Repo: src, Path: "commands/foo.md", Kind: "command"}

	dest, err := Install(context.Background(), e, dir)
	if err != nil {
		t.Fatalf("install command: %v", err)
	}
	if want := filepath.Join(dir, "foo.md"); dest != want {
		t.Fatalf("dest = %s, want %s", dest, want)
	}
	if info, err := os.Stat(dest); err != nil || info.IsDir() {
		t.Fatalf("expected a single file at %s", dest)
	}
	rec, ok, _ := ReadRecord(dir, "foo")
	if !ok || rec.Kind != "command" || rec.Artifact != "foo.md" || rec.Cksum == "" {
		t.Fatalf("record = %+v", rec)
	}
	if names, _ := ListInstalled(dir, "command"); len(names) != 1 || names[0] != "foo.md" {
		t.Fatalf("ListInstalled = %v, want [foo.md]", names)
	}
	if err := Remove("foo", dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("command file not removed")
	}

	// A command whose path is a directory is rejected.
	src2 := gitRepoWith(t, map[string]string{"commands/sub/x.md": "y\n"})
	bad := registry.Entry{Name: "sub", Description: "d", Author: "t", Repo: src2, Path: "commands/sub", Kind: "command"}
	if _, err := Install(context.Background(), bad, t.TempDir()); err == nil {
		t.Fatal("a directory command path should be rejected")
	}
}

func TestInstallAgentAndOutputStyle(t *testing.T) {
	src := gitRepoWith(t, map[string]string{
		"agents/reviewer.md": "---\nname: reviewer\ndescription: reviews code\n---\nbody\n",
		"styles/terse.md":    "---\nname: terse\n---\nbe terse\n",
	})
	for _, tc := range []struct{ kind, path, name string }{
		{"agent", "agents/reviewer.md", "reviewer"},
		{"output-style", "styles/terse.md", "terse"},
	} {
		dir := t.TempDir()
		e := registry.Entry{Name: tc.name, Description: "d", Author: "t", Repo: src, Path: tc.path, Kind: tc.kind}
		dest, err := Install(context.Background(), e, dir)
		if err != nil {
			t.Fatalf("install %s: %v", tc.kind, err)
		}
		if want := filepath.Join(dir, tc.name+".md"); dest != want {
			t.Fatalf("%s dest = %s, want %s", tc.kind, dest, want)
		}
		if info, err := os.Stat(dest); err != nil || info.IsDir() {
			t.Fatalf("%s should be a single .md file", tc.kind)
		}
		rec, ok, _ := ReadRecord(dir, tc.name)
		if !ok || rec.Kind != tc.kind || rec.Artifact != tc.name+".md" {
			t.Fatalf("%s record = %+v", tc.kind, rec)
		}
		if names, _ := ListInstalled(dir, tc.kind); len(names) != 1 || names[0] != tc.name+".md" {
			t.Fatalf("%s ListInstalled = %v", tc.kind, names)
		}
		if err := Remove(tc.name, dir); err != nil {
			t.Fatalf("remove %s: %v", tc.kind, err)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("%s not removed", tc.kind)
		}
	}
}

func TestInstallHookRegistersAndUnregisters(t *testing.T) {
	src := gitRepoWith(t, map[string]string{"hooks/greet.sh": "#!/usr/bin/env bash\necho hi\n"})
	base := t.TempDir()
	dir := filepath.Join(base, "hooks")
	e := registry.Entry{
		Name: "greet", Description: "d", Author: "t", Repo: src,
		Path: "hooks/greet.sh", Kind: "hook",
		Hook: &registry.HookSpec{Event: "SessionStart"},
	}

	dest, err := Install(context.Background(), e, dir)
	if err != nil {
		t.Fatalf("install hook: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("hook script is not executable: %v", info.Mode())
	}

	settings := filepath.Join(base, "settings.json")
	abs, _ := filepath.Abs(dest)
	if cmds := commandsIn(t, settings, "SessionStart", ""); len(cmds) != 1 || cmds[0] != abs {
		t.Fatalf("registration = %v, want [%s]", cmds, abs)
	}
	rec, ok, _ := ReadRecord(dir, "greet")
	if !ok || rec.Kind != "hook" || rec.Artifact != "greet.sh" || rec.Hook == nil || rec.Hook.Event != "SessionStart" {
		t.Fatalf("record = %+v", rec)
	}

	if err := Remove("greet", dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("hook script not removed")
	}
	if cmds := commandsIn(t, settings, "SessionStart", ""); len(cmds) != 0 {
		t.Fatalf("hook still registered after remove: %v", cmds)
	}

	// A hook entry with no spec is rejected before anything is written.
	noSpec := e
	noSpec.Hook = nil
	if _, err := Install(context.Background(), noSpec, t.TempDir()); err == nil {
		t.Fatal("a hook with no spec should be rejected")
	}
}

func TestInstallHookExtensionChangeCleansUp(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "hooks")
	settings := filepath.Join(base, "settings.json")

	src1 := gitRepoWith(t, map[string]string{"hooks/greet.sh": "#!/usr/bin/env bash\necho hi\n"})
	e1 := registry.Entry{Name: "greet", Description: "d", Author: "t", Repo: src1, Path: "hooks/greet.sh", Kind: "hook", Hook: &registry.HookSpec{Event: "SessionStart"}}
	if _, err := Install(context.Background(), e1, dir); err != nil {
		t.Fatalf("install .sh: %v", err)
	}

	// Reinstalling the same hook from a .py source must drop the old .sh file and
	// its settings.json registration, not orphan them.
	src2 := gitRepoWith(t, map[string]string{"hooks/greet.py": "#!/usr/bin/env python3\nprint('hi')\n"})
	e2 := registry.Entry{Name: "greet", Description: "d", Author: "t", Repo: src2, Path: "hooks/greet.py", Kind: "hook", Hook: &registry.HookSpec{Event: "SessionStart"}}
	if _, err := Install(context.Background(), e2, dir); err != nil {
		t.Fatalf("install .py: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "greet.sh")); !os.IsNotExist(err) {
		t.Fatal("old greet.sh should have been removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "greet.py")); err != nil {
		t.Fatalf("new greet.py should exist: %v", err)
	}
	newAbs, _ := filepath.Abs(filepath.Join(dir, "greet.py"))
	cmds := commandsIn(t, settings, "SessionStart", "")
	if len(cmds) != 1 || cmds[0] != newAbs {
		t.Fatalf("registration = %v, want only [%s]", cmds, newAbs)
	}
}

func TestDiagnoseRecordlessFileNotBroken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "loose.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Under a --dir override the kind is unknown (""); a present file with no
	// record is "no record" (a warning), not "recorded but not installed".
	diags, err := Diagnose(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].Status != StatusNoRecord {
		t.Fatalf("record-less file diagnosed as %+v, want StatusNoRecord", diags)
	}
}
