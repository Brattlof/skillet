package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commandsIn returns the command strings registered under an (event, matcher) in a
// settings.json, so tests can assert on the registration without caring about the
// surrounding structure.
func commandsIn(t *testing.T, path, event, matcher string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	var out []string
	hooks, _ := m["hooks"].(map[string]any)
	arr, _ := hooks[event].([]any)
	for _, it := range arr {
		block, _ := it.(map[string]any)
		if matcherOf(block) != matcher {
			continue
		}
		list, _ := block["hooks"].([]any)
		for _, h := range list {
			if hm, ok := h.(map[string]any); ok && hm["type"] == "command" {
				out = append(out, hm["command"].(string))
			}
		}
	}
	return out
}

func TestRegisterHookCreatesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	if err := registerHook(path, "SessionStart", "", "/h/a.sh"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := commandsIn(t, path, "SessionStart", ""); len(got) != 1 || got[0] != "/h/a.sh" {
		t.Fatalf("after register = %v, want [/h/a.sh]", got)
	}

	// Registering the same command again must not duplicate it or rewrite the file.
	before, _ := os.ReadFile(path)
	if err := registerHook(path, "SessionStart", "", "/h/a.sh"); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("re-registering an identical hook rewrote the file")
	}
	if got := commandsIn(t, path, "SessionStart", ""); len(got) != 1 {
		t.Fatalf("re-register duplicated the hook: %v", got)
	}
}

func TestRegisterHookPreservesOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := map[string]any{
		"model": "opus",
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks":   []any{map[string]any{"type": "command", "command": "/existing.sh"}},
				},
			},
		},
	}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := registerHook(path, "PreToolUse", "Bash", "/mine.sh"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// The existing command and the new one share the Bash block.
	got := commandsIn(t, path, "PreToolUse", "Bash")
	if len(got) != 2 {
		t.Fatalf("Bash block = %v, want 2 commands", got)
	}
	// An unrelated top-level key must survive.
	raw, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "opus" {
		t.Fatalf("unrelated key lost: model = %v", m["model"])
	}
}

func TestUnregisterHookPrunesEmptyStructures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := registerHook(path, "PreToolUse", "Bash", "/a.sh"); err != nil {
		t.Fatal(err)
	}
	if err := registerHook(path, "PreToolUse", "Bash", "/b.sh"); err != nil {
		t.Fatal(err)
	}

	// Removing one of two leaves the block intact.
	if err := unregisterHook(path, "PreToolUse", "Bash", "/a.sh"); err != nil {
		t.Fatalf("unregister a: %v", err)
	}
	if got := commandsIn(t, path, "PreToolUse", "Bash"); len(got) != 1 || got[0] != "/b.sh" {
		t.Fatalf("after removing a = %v, want [/b.sh]", got)
	}

	// Removing the last one prunes the block, the event, and the hooks object.
	if err := unregisterHook(path, "PreToolUse", "Bash", "/b.sh"); err != nil {
		t.Fatalf("unregister b: %v", err)
	}
	raw, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["hooks"]; ok {
		t.Fatalf("empty hooks object should be pruned, got %v", m["hooks"])
	}

	// Unregistering from a missing file is a no-op.
	if err := unregisterHook(filepath.Join(t.TempDir(), "nope.json"), "SessionStart", "", "/x"); err != nil {
		t.Fatalf("unregister missing file should be a no-op: %v", err)
	}
}

func TestHookRegisteredReportsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if reg, err := hookRegistered(path, "SessionStart", "", "/a.sh"); err != nil || reg {
		t.Fatalf("missing file: reg=%v err=%v, want false/nil", reg, err)
	}
	if err := registerHook(path, "SessionStart", "", "/a.sh"); err != nil {
		t.Fatal(err)
	}
	if reg, err := hookRegistered(path, "SessionStart", "", "/a.sh"); err != nil || !reg {
		t.Fatalf("after register: reg=%v err=%v, want true/nil", reg, err)
	}
	if reg, _ := hookRegistered(path, "SessionStart", "", "/other.sh"); reg {
		t.Fatal("a different command should not report registered")
	}
}

func TestRegisterHookPreservesLargeNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// A hand-added key with an integer beyond 2^53 must survive a hook edit
	// byte-for-byte rather than being rounded through float64.
	seed := "{\n  \"bigId\": 12345678901234567890,\n  \"exact\": 9007199254740993\n}\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := registerHook(path, "SessionStart", "", "/a.sh"); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, _ := os.ReadFile(path)
	for _, want := range []string{"12345678901234567890", "9007199254740993"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("number %s was altered by the round-trip:\n%s", want, got)
		}
	}
}

func TestLoadSettingsRejectsNonObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("[1,2,3]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(path); err == nil {
		t.Fatal("a non-object settings.json should be rejected, not silently clobbered")
	}
}
