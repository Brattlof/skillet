package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// decodeSettings unmarshals settings JSON with UseNumber so numeric values in
// keys skillet does not touch round-trip back byte-for-byte instead of being
// coerced through float64 (which would round integers above 2^53).
func decodeSettings(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// settingsPath returns the settings.json that sits next to a hooks directory.
// For the standard ~/.claude/hooks this is ~/.claude/settings.json; for a --dir
// override it sits beside the override so tests and custom layouts stay self
// contained.
func settingsPath(hooksDir string) string {
	return filepath.Join(filepath.Dir(hooksDir), "settings.json")
}

// loadSettings reads settings.json into a generic object. A missing file yields
// an empty object so registration can start from scratch. A file whose top level
// is not a JSON object is an error, since rewriting it would discard the content.
func loadSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := decodeSettings(b)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// saveSettings writes the settings object back atomically with two-space indent.
// Marshaling a map sorts keys, so unrelated keys are preserved but may be
// reordered; their values are kept intact.
func saveSettings(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

func matcherOf(block map[string]any) string {
	if s, ok := block["matcher"].(string); ok {
		return s
	}
	return ""
}

// registerHook adds a command hook for (event, matcher) to settings.json,
// preserving every other key. It is idempotent: re-registering the same command
// under the same event and matcher does not duplicate it and does not rewrite the
// file. An empty matcher is stored as an omitted key, which Claude Code treats as
// matching every occurrence.
func registerHook(path, event, matcher, command string) error {
	m, err := loadSettings(path)
	if err != nil {
		return err
	}

	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		if m["hooks"] != nil {
			return fmt.Errorf("%s: \"hooks\" is not an object", path)
		}
		hooks = map[string]any{}
	}

	var arr []any
	if existing, ok := hooks[event].([]any); ok {
		arr = existing
	} else if hooks[event] != nil {
		return fmt.Errorf("%s: hooks.%s is not an array", path, event)
	}

	var block map[string]any
	for _, it := range arr {
		if b, ok := it.(map[string]any); ok && matcherOf(b) == matcher {
			block = b
			break
		}
	}
	created := false
	if block == nil {
		block = map[string]any{}
		if matcher != "" {
			block["matcher"] = matcher
		}
		block["hooks"] = []any{}
		arr = append(arr, block)
		created = true
	}

	list, _ := block["hooks"].([]any)
	for _, h := range list {
		if hm, ok := h.(map[string]any); ok && hm["type"] == "command" && hm["command"] == command {
			if created {
				break // a fresh block still needs to be written back
			}
			return nil // already present, leave the file untouched
		}
	}
	list = append(list, map[string]any{"type": "command", "command": command})
	block["hooks"] = list
	hooks[event] = arr
	m["hooks"] = hooks
	return saveSettings(path, m)
}

// unregisterHook removes the command hook for (event, matcher) from settings.json
// and prunes any block, event, or the hooks object left empty. A missing file or
// absent registration is a no-op.
func unregisterHook(path, event, matcher, command string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	m, err := decodeSettings(b)
	if err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		return nil
	}
	arr, ok := hooks[event].([]any)
	if !ok {
		return nil
	}

	changed := false
	kept := make([]any, 0, len(arr))
	for _, it := range arr {
		block, ok := it.(map[string]any)
		if !ok || matcherOf(block) != matcher {
			kept = append(kept, it)
			continue
		}
		list, _ := block["hooks"].([]any)
		keptHooks := make([]any, 0, len(list))
		for _, h := range list {
			if hm, ok := h.(map[string]any); ok && hm["type"] == "command" && hm["command"] == command {
				changed = true
				continue
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) == 0 {
			continue // drop a block with no hooks left
		}
		block["hooks"] = keptHooks
		kept = append(kept, block)
	}
	if !changed {
		return nil
	}
	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	if len(hooks) == 0 {
		delete(m, "hooks")
	} else {
		m["hooks"] = hooks
	}
	return saveSettings(path, m)
}

// hookRegistered reports whether a command hook for (event, matcher) is present
// in settings.json. It is used by doctor to flag a hook that was installed but
// lost its registration.
func hookRegistered(path, event, matcher, command string) (bool, error) {
	m, err := loadSettings(path)
	if err != nil {
		return false, err
	}
	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		return false, nil
	}
	arr, ok := hooks[event].([]any)
	if !ok {
		return false, nil
	}
	for _, it := range arr {
		block, ok := it.(map[string]any)
		if !ok || matcherOf(block) != matcher {
			continue
		}
		list, _ := block["hooks"].([]any)
		for _, h := range list {
			if hm, ok := h.(map[string]any); ok && hm["type"] == "command" && hm["command"] == command {
				return true, nil
			}
		}
	}
	return false, nil
}
