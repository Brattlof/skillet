package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Brattlof/skillet/internal/registry"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func serverIn(t *testing.T, path, topKey, name string) map[string]any {
	t.Helper()
	servers, _ := readJSON(t, path)[topKey].(map[string]any)
	s, ok := servers[name].(map[string]any)
	if !ok {
		t.Fatalf("%s not found under %s in %s", name, topKey, path)
	}
	return s
}

func TestInstallMCPStdio(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := registry.Entry{Name: "ctx", Kind: "mcp", MCP: &registry.MCPSpec{
		Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"K": "v"},
	}}
	p, err := InstallMCP(e, "cursor")
	if err != nil {
		t.Fatal(err)
	}
	s := serverIn(t, p, "mcpServers", "ctx")
	if s["command"] != "npx" {
		t.Fatalf("command = %v", s["command"])
	}
	if _, ok := s["type"]; ok {
		t.Fatal("a stdio entry must not carry a type field")
	}
	if args, _ := s["args"].([]any); len(args) != 2 || args[0] != "-y" {
		t.Fatalf("args = %v", s["args"])
	}
}

func TestInstallMCPRemotePerClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := registry.Entry{Name: "remote", Kind: "mcp", MCP: &registry.MCPSpec{URL: "https://mcp.example.com/mcp"}}

	p, _ := InstallMCP(e, "cursor")
	s := serverIn(t, p, "mcpServers", "remote")
	if s["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("cursor url = %v", s["url"])
	}
	if _, ok := s["type"]; ok {
		t.Fatal("cursor remote must not set type")
	}

	p, _ = InstallMCP(e, "windsurf")
	s = serverIn(t, p, "mcpServers", "remote")
	if s["serverUrl"] != "https://mcp.example.com/mcp" {
		t.Fatalf("windsurf must use serverUrl, got %v", s)
	}

	p, _ = InstallMCP(e, "gemini")
	s = serverIn(t, p, "mcpServers", "remote")
	if s["httpUrl"] != "https://mcp.example.com/mcp" {
		t.Fatalf("gemini must use httpUrl, got %v", s)
	}
}

func TestInstallMCPClaudeRemoteHasType(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	e := registry.Entry{Name: "remote", Kind: "mcp", MCP: &registry.MCPSpec{URL: "https://mcp.example.com/mcp"}}
	p, err := InstallMCP(e, "claude")
	if err != nil {
		t.Fatal(err)
	}
	s := serverIn(t, filepath.Join(dir, p), "mcpServers", "remote")
	if s["type"] != "http" {
		t.Fatalf("claude remote should set type http, got %v", s["type"])
	}
	if s["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("claude url = %v", s["url"])
	}
}

func TestMCPPreserveRemoveList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := registry.Entry{Name: "a", Kind: "mcp", MCP: &registry.MCPSpec{Command: "x"}}
	b := registry.Entry{Name: "b", Kind: "mcp", MCP: &registry.MCPSpec{Command: "y"}}
	if _, err := InstallMCP(a, "cursor"); err != nil {
		t.Fatal(err)
	}

	// An unrelated key must survive the next write.
	p, _ := mcpClients["cursor"].path()
	m := readJSON(t, p)
	m["extra"] = "keep"
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallMCP(b, "cursor"); err != nil {
		t.Fatal(err)
	}

	names, _, err := ListMCP("cursor")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("list = %v, want [a b]", names)
	}
	if readJSON(t, p)["extra"] != "keep" {
		t.Fatal("an unrelated key was dropped")
	}

	if _, removed, err := RemoveMCP("a", "cursor"); err != nil || !removed {
		t.Fatalf("remove a: removed=%v err=%v", removed, err)
	}
	if names, _, _ := ListMCP("cursor"); len(names) != 1 || names[0] != "b" {
		t.Fatalf("after remove = %v, want [b]", names)
	}
	if _, removed, _ := RemoveMCP("missing", "cursor"); removed {
		t.Fatal("removing an absent server should report not removed")
	}

	if _, err := InstallMCP(a, "bogus"); err == nil {
		t.Fatal("an unknown client should error")
	}
}
