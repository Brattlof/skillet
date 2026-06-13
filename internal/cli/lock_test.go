package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Brattlof/skillet/internal/install"
	"github.com/Brattlof/skillet/internal/registry"
)

func makeGitRepo(t *testing.T, files map[string]string) string {
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
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
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

func TestRestorePrunesAndFrozenVerifies(t *testing.T) {
	src := makeGitRepo(t, map[string]string{
		"examples/a/SKILL.md": "# a\n",
		"examples/b/SKILL.md": "# b\n",
		"examples/c/SKILL.md": "# c\n",
	})
	override := t.TempDir()
	ctx := context.Background()
	mk := func(name string) registry.Entry {
		return registry.Entry{Name: name, Description: "d", Author: "t", Repo: src, Path: "examples/" + name}
	}
	for _, n := range []string{"a", "b"} {
		if _, err := install.Install(ctx, mk(n), override); err != nil {
			t.Fatalf("install %s: %v", n, err)
		}
	}

	// Lockfile pins only "a".
	recA, ok, err := install.ReadRecord(override, "a")
	if err != nil || !ok {
		t.Fatalf("read record a: ok=%v err=%v", ok, err)
	}
	lockfile := filepath.Join(t.TempDir(), "skillet.lock")
	var lf install.Lockfile
	lf.Upsert(install.LockEntry{Name: recA.Name, Kind: recA.Kind, Repo: recA.Repo, Path: recA.Path, Commit: recA.Commit, Cksum: recA.Cksum})
	if err := install.WriteLock(lockfile, lf); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKILLET_LOCKFILE", lockfile)

	// Restore reinstalls "a" and prunes "b" (managed but not in the lock).
	if err := restoreFromLock(ctx, "claude", override); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(override, "a")); err != nil {
		t.Fatal("a should remain installed")
	}
	if _, err := os.Stat(filepath.Join(override, "b")); !os.IsNotExist(err) {
		t.Fatal("b should have been pruned")
	}
	if _, ok, _ := install.ReadRecord(override, "b"); ok {
		t.Fatal("b's record should be gone after prune")
	}

	// The install now matches the lock.
	if err := verifyLock("claude", override); err != nil {
		t.Fatalf("frozen verify should pass: %v", err)
	}

	// An extra managed install makes --frozen fail.
	if _, err := install.Install(ctx, mk("c"), override); err != nil {
		t.Fatalf("install c: %v", err)
	}
	if err := verifyLock("claude", override); err == nil {
		t.Fatal("frozen verify should fail when an install is not in the lock")
	}
}

// Under the agents target a command or hook entry is not routable. Restore skips
// it (non-fatal); frozen verify must agree and skip it too, not report it broken.
func TestRestoreAndVerifyAgreeUnderAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lockfile := filepath.Join(t.TempDir(), "skillet.lock")
	var lf install.Lockfile
	lf.Upsert(install.LockEntry{Name: "cmd", Kind: "command", Repo: "https://github.com/x/y", Path: "p", Commit: "abcabc"})
	if err := install.WriteLock(lockfile, lf); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKILLET_LOCKFILE", lockfile)

	if err := restoreFromLock(context.Background(), "agents", ""); err != nil {
		t.Fatalf("restore should skip the command entry, not error: %v", err)
	}
	if err := verifyLock("agents", ""); err != nil {
		t.Fatalf("verify should agree and skip the command entry, not fail: %v", err)
	}
}
