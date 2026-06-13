package install

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Brattlof/skillet/internal/registry"
)

// mcpClient describes where and how a tool stores MCP servers. All supported
// clients use the canonical name-keyed JSON map; they differ only in the file
// path, the top-level key, the remote-URL field name, and whether a remote entry
// carries an explicit "type": "http".
type mcpClient struct {
	topKey     string
	urlField   string
	remoteType bool
	path       func() (string, error)
}

func homeJoin(parts ...string) func() (string, error) {
	return func() (string, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(append([]string{home}, parts...)...), nil
	}
}

// mcpClients are the tools whose MCP config is the canonical JSON map. Claude
// writes the project-scoped .mcp.json (its user file ~/.claude.json is stateful
// and best left to `claude mcp add`).
var mcpClients = map[string]mcpClient{
	"claude":   {topKey: "mcpServers", urlField: "url", remoteType: true, path: func() (string, error) { return ".mcp.json", nil }},
	"cursor":   {topKey: "mcpServers", urlField: "url", path: homeJoin(".cursor", "mcp.json")},
	"windsurf": {topKey: "mcpServers", urlField: "serverUrl", path: homeJoin(".codeium", "windsurf", "mcp_config.json")},
	"gemini":   {topKey: "mcpServers", urlField: "httpUrl", path: homeJoin(".gemini", "settings.json")},
	"cline":    {topKey: "mcpServers", urlField: "url", path: homeJoin(".cline", "mcp.json")},
}

// ValidMCPClient reports whether client is a supported MCP target.
func ValidMCPClient(client string) bool {
	_, ok := mcpClients[client]
	return ok
}

// MCPClients returns the supported client names, sorted, for help and errors.
func MCPClients() []string {
	out := make([]string, 0, len(mcpClients))
	for k := range mcpClients {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lookupMCPClient(client string) (mcpClient, error) {
	c, ok := mcpClients[client]
	if !ok {
		return mcpClient{}, fmt.Errorf("unknown mcp client %q (want %s)", client, strings.Join(MCPClients(), ", "))
	}
	return c, nil
}

// mcpEntry builds the server object for a client from a spec.
func mcpEntry(c mcpClient, s *registry.MCPSpec) map[string]any {
	m := map[string]any{}
	if strings.TrimSpace(s.Command) != "" {
		m["command"] = s.Command
		if len(s.Args) > 0 {
			args := make([]any, len(s.Args))
			for i, a := range s.Args {
				args[i] = a
			}
			m["args"] = args
		}
		if len(s.Env) > 0 {
			env := make(map[string]any, len(s.Env))
			for k, v := range s.Env {
				env[k] = v
			}
			m["env"] = env
		}
		return m
	}
	if c.remoteType {
		m["type"] = "http"
	}
	m[c.urlField] = s.URL
	return m
}

// InstallMCP registers the entry's server in the client's MCP config, preserving
// every other server and key, and returns the config path it wrote.
func InstallMCP(e registry.Entry, client string) (string, error) {
	c, err := lookupMCPClient(client)
	if err != nil {
		return "", err
	}
	if e.MCP == nil {
		return "", fmt.Errorf("mcp entry %q has no server spec", e.Name)
	}
	if !safeName(e.Name) {
		return "", fmt.Errorf("unsafe mcp server name %q", e.Name)
	}
	p, err := c.path()
	if err != nil {
		return "", err
	}
	m, err := loadSettings(p)
	if err != nil {
		return "", err
	}
	servers, ok := m[c.topKey].(map[string]any)
	if !ok {
		if m[c.topKey] != nil {
			return "", fmt.Errorf("%s: %q is not an object", p, c.topKey)
		}
		servers = map[string]any{}
	}
	servers[e.Name] = mcpEntry(c, e.MCP)
	m[c.topKey] = servers
	if err := saveSettings(p, m); err != nil {
		return "", err
	}
	return p, nil
}

// RemoveMCP deletes a server from the client's MCP config and prunes the map if it
// becomes empty. It returns the config path and whether the server was present.
func RemoveMCP(name, client string) (path string, removed bool, err error) {
	c, err := lookupMCPClient(client)
	if err != nil {
		return "", false, err
	}
	p, err := c.path()
	if err != nil {
		return "", false, err
	}
	if _, serr := os.Stat(p); os.IsNotExist(serr) {
		return p, false, nil
	}
	m, err := loadSettings(p)
	if err != nil {
		return "", false, err
	}
	servers, ok := m[c.topKey].(map[string]any)
	if !ok {
		return p, false, nil
	}
	if _, present := servers[name]; !present {
		return p, false, nil
	}
	delete(servers, name)
	if len(servers) == 0 {
		delete(m, c.topKey)
	} else {
		m[c.topKey] = servers
	}
	if err := saveSettings(p, m); err != nil {
		return "", false, err
	}
	return p, true, nil
}

// ListMCP returns the server names registered with a client, sorted, and the
// config path inspected.
func ListMCP(client string) (names []string, path string, err error) {
	c, err := lookupMCPClient(client)
	if err != nil {
		return nil, "", err
	}
	p, err := c.path()
	if err != nil {
		return nil, "", err
	}
	m, err := loadSettings(p)
	if err != nil {
		return nil, p, err
	}
	servers, _ := m[c.topKey].(map[string]any)
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, p, nil
}
