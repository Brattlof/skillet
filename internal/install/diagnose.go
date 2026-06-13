package install

import (
	"os"
	"path/filepath"
	"sort"
)

// Status is the health of a single installed skill.
type Status string

const (
	StatusOK             Status = "ok"
	StatusMissingSkillMD Status = "missing SKILL.md"
	StatusDrift          Status = "modified since install"
	StatusBroken         Status = "recorded but not installed"
	StatusNoRecord       Status = "installed without a manifest record"
)

// Diagnosis is the result of checking one skill.
type Diagnosis struct {
	Name   string
	Status Status
}

// Diagnose checks every installed skill in dir against its manifest record:
// a missing install, a missing SKILL.md, content that has drifted from the
// recorded checksum, or an install with no provenance record. It does not touch
// the network; registry membership is checked by the caller.
func Diagnose(dir string) ([]Diagnosis, error) {
	recs, err := Records(dir)
	if err != nil {
		return nil, err
	}
	recByName := make(map[string]Record, len(recs))
	names := make(map[string]bool, len(recs))
	for _, r := range recs {
		recByName[r.Name] = r
		names[r.Name] = true
	}

	installed, err := ListInstalled(dir)
	if err != nil {
		return nil, err
	}
	for _, n := range installed {
		names[n] = true
	}

	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)

	out := make([]Diagnosis, 0, len(ordered))
	for _, n := range ordered {
		rec, hasRec := recByName[n]
		info, statErr := os.Stat(filepath.Join(dir, n))
		dirExists := statErr == nil && info.IsDir()

		status := StatusOK
		switch {
		case !dirExists:
			status = StatusBroken
		case !fileExists(filepath.Join(dir, n, "SKILL.md")):
			status = StatusMissingSkillMD
		case hasRec && rec.Cksum != "":
			sum, err := hashTree(filepath.Join(dir, n))
			if err != nil {
				return nil, err
			}
			if sum != rec.Cksum {
				status = StatusDrift
			}
		case !hasRec:
			status = StatusNoRecord
		}
		out = append(out, Diagnosis{Name: n, Status: status})
	}
	return out, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
