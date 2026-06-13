// Package registry loads and queries the embedded skill index.
package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	skillet "github.com/Brattlof/skillet"
)

// Entry is a single curated skill in the registry.
type Entry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Repo        string   `json:"repo"`
	Path        string   `json:"path"`
	Author      string   `json:"author"`
	Tags        []string `json:"tags"`
}

// Load parses the embedded registry, sorted by name.
func Load() ([]Entry, error) {
	var entries []Entry
	if err := json.Unmarshal(skillet.RegistryJSON, &entries); err != nil {
		return nil, fmt.Errorf("parsing registry: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Find returns the entry whose name matches (case-insensitively).
func Find(name string) (Entry, bool, error) {
	entries, err := Load()
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// Search returns entries matching query in name, description, or tags.
// An empty query returns every entry.
func Search(query string) ([]Entry, error) {
	entries, err := Load()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return entries, nil
	}
	var out []Entry
	for _, e := range entries {
		hay := strings.ToLower(e.Name + " " + e.Description + " " + strings.Join(e.Tags, " "))
		if strings.Contains(hay, q) {
			out = append(out, e)
		}
	}
	return out, nil
}
