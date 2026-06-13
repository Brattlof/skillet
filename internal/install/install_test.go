package install

import (
	"os"
	"path/filepath"
	"testing"
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

	// empty dir -> no skills
	names, err := ListInstalled(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty, got %v", names)
	}

	// create a fake installed skill
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
