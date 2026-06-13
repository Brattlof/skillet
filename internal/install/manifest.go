package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// metaDirName is the folder, inside a skills directory, that holds one provenance
// record per installed skill. It is hidden so it never shows up as a skill.
const metaDirName = ".skillet"

// Record is the provenance written when a skill is installed. It lets update,
// doctor, and list know where a skill came from and whether it has drifted.
//
// Fields added later MUST stay omitempty so an older skillet can read a record
// written by a newer one and vice versa.
type Record struct {
	Name        string    `json:"name"`
	Repo        string    `json:"repo"`
	Path        string    `json:"path"`
	Kind        string    `json:"kind,omitempty"`   // skill (default), command, or hook
	Ref         string    `json:"ref,omitempty"`    // ref requested by the registry entry, if any
	Commit      string    `json:"commit,omitempty"` // commit actually installed
	Cksum       string    `json:"cksum,omitempty"`  // sha256 tree hash of the installed content
	InstalledAt time.Time `json:"installed_at"`
}

func metaDir(dir string) string        { return filepath.Join(dir, metaDirName) }
func metaPath(dir, name string) string { return filepath.Join(metaDir(dir), name+".json") }

// writeRecord stores r under dir/.skillet/<name>.json atomically.
func writeRecord(dir string, r Record) error {
	if err := os.MkdirAll(metaDir(dir), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(metaPath(dir, r.Name), append(b, '\n'))
}

// removeRecord deletes the provenance record for name. A missing record is a no-op.
func removeRecord(dir, name string) error {
	err := os.Remove(metaPath(dir, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ReadRecord returns the provenance for a single installed skill.
func ReadRecord(dir, name string) (Record, bool, error) {
	b, err := os.ReadFile(metaPath(dir, name))
	if os.IsNotExist(err) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, false, err
	}
	return r, true, nil
}

// Records returns the provenance of every recorded skill in dir, sorted by name.
func Records(dir string) ([]Record, error) {
	entries, err := os.ReadDir(metaDir(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(metaDir(dir), e.Name()))
		if err != nil {
			return nil, err
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// writeFileAtomic writes b to path via a temp file and rename, so a crash never
// leaves a half-written record.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
