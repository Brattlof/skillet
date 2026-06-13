package install

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// LockEntry is a fully resolved install spec: enough to reproduce an install
// without consulting the registry.
type LockEntry struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Commit string `json:"commit,omitempty"`
	Cksum  string `json:"cksum,omitempty"`
}

// Lockfile pins an exact set of skills for reproducible installs.
type Lockfile struct {
	Skills []LockEntry `json:"skills"`
}

// ReadLock loads a lockfile. A missing file yields an empty lockfile, not an error.
func ReadLock(path string) (Lockfile, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Lockfile{}, nil
	}
	if err != nil {
		return Lockfile{}, err
	}
	var lf Lockfile
	if err := json.Unmarshal(b, &lf); err != nil {
		return Lockfile{}, err
	}
	return lf, nil
}

// WriteLock writes the lockfile atomically, sorted by name for stable diffs.
func WriteLock(path string, lf Lockfile) error {
	sort.Slice(lf.Skills, func(i, j int) bool { return lf.Skills[i].Name < lf.Skills[j].Name })
	b, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// Upsert replaces the entry with the same name (case-insensitive) or appends it.
func (lf *Lockfile) Upsert(e LockEntry) {
	for i := range lf.Skills {
		if strings.EqualFold(lf.Skills[i].Name, e.Name) {
			lf.Skills[i] = e
			return
		}
	}
	lf.Skills = append(lf.Skills, e)
}
