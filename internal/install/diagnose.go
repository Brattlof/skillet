package install

import (
	"os"
	"path/filepath"
	"sort"
)

// Status is the health of a single installed artifact.
type Status string

const (
	StatusOK               Status = "ok"
	StatusMissingSkillMD   Status = "missing SKILL.md"
	StatusDrift            Status = "modified since install"
	StatusBroken           Status = "recorded but not installed"
	StatusNoRecord         Status = "installed without a manifest record"
	StatusHookUnregistered Status = "installed but not registered in settings.json"
)

// Diagnosis is the result of checking one artifact.
type Diagnosis struct {
	Name   string
	Status Status
}

// Diagnose checks every installed artifact in dir against its manifest record: a
// missing install, a skill missing its SKILL.md, content that has drifted from the
// recorded checksum, a hook that lost its settings.json registration, or an
// install with no provenance record. kind tells it the artifact shape to expect
// (skill directories vs command/hook files); an empty kind accepts either. It does
// not touch the network; registry membership is checked by the caller.
func Diagnose(dir, kind string) ([]Diagnosis, error) {
	recs, err := Records(dir)
	if err != nil {
		return nil, err
	}

	type item struct {
		name     string
		artifact string
		rec      Record
		hasRec   bool
	}
	var items []item
	recArtifacts := make(map[string]bool, len(recs))
	for _, r := range recs {
		a := r.ArtifactName()
		recArtifacts[a] = true
		items = append(items, item{name: r.Name, artifact: a, rec: r, hasRec: true})
	}

	listed, err := ListInstalled(dir, kind)
	if err != nil {
		return nil, err
	}
	for _, n := range listed {
		if recArtifacts[n] {
			continue // already covered by a record
		}
		items = append(items, item{name: n, artifact: n})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })

	out := make([]Diagnosis, 0, len(items))
	for _, it := range items {
		status, err := diagnoseOne(dir, kind, it.artifact, it.rec, it.hasRec)
		if err != nil {
			return nil, err
		}
		out = append(out, Diagnosis{Name: it.name, Status: status})
	}
	return out, nil
}

func diagnoseOne(dir, dirKind, artifact string, rec Record, hasRec bool) (Status, error) {
	kind := dirKind
	if hasRec && rec.Kind != "" {
		kind = rec.Kind
	}

	dest := filepath.Join(dir, artifact)
	info, statErr := os.Stat(dest)
	exists := statErr == nil

	switch kind {
	case "skill":
		if !exists || !info.IsDir() {
			return StatusBroken, nil // missing, or a file where a directory was recorded
		}
		if !fileExists(filepath.Join(dest, "SKILL.md")) {
			return StatusMissingSkillMD, nil
		}
	case "command", "hook":
		if !exists || info.IsDir() {
			return StatusBroken, nil
		}
	default:
		// Unknown kind (a --dir override with no record to say what it is): we
		// cannot judge the shape, so only a missing artifact is broken.
		if !exists {
			return StatusBroken, nil
		}
	}

	if hasRec && rec.Cksum != "" {
		sum, err := hashArtifact(dest)
		if err != nil {
			return "", err
		}
		if sum != rec.Cksum {
			return StatusDrift, nil
		}
	}

	if kind == "hook" && hasRec && rec.Hook != nil {
		abs, aerr := filepath.Abs(dest)
		if aerr != nil {
			abs = dest
		}
		reg, err := hookRegistered(settingsPath(dir), rec.Hook.Event, rec.Hook.Matcher, abs)
		if err != nil {
			return "", err
		}
		if !reg {
			return StatusHookUnregistered, nil
		}
	}

	if !hasRec {
		return StatusNoRecord, nil
	}
	return StatusOK, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
