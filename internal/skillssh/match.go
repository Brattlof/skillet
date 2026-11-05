package skillssh

import (
	"path"
	"sort"
	"strings"
)

// Frontmatter is the part of a SKILL.md header skillet reads.
type Frontmatter struct {
	Name        string
	Description string
}

// maxFrontmatter bounds how much of a SKILL.md is scanned for its header.
const maxFrontmatter = 64 << 10

// ParseFrontmatter reads the name and description from a SKILL.md YAML header.
// It handles the shapes skills use in practice - plain, quoted, and folded or
// literal block scalars - without a YAML dependency. Values are cleaned of
// control characters. A file with no header yields a zero Frontmatter.
func ParseFrontmatter(doc string) Frontmatter {
	if len(doc) > maxFrontmatter {
		doc = doc[:maxFrontmatter]
	}
	doc = strings.TrimPrefix(doc, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Frontmatter{}
	}
	var fm Frontmatter
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			break
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue // only top-level keys
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "name" && key != "description" {
			continue
		}
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
			// A block scalar: the value is the following more-indented lines.
			var parts []string
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.TrimSpace(next) != "" && next[0] != ' ' && next[0] != '\t' {
					break
				}
				parts = append(parts, strings.TrimSpace(next))
				i++
			}
			val = strings.Join(parts, " ")
		} else {
			val = unquote(val)
		}
		val = Clean(val)
		if key == "name" {
			fm.Name = val
		} else {
			fm.Description = val
		}
	}
	return fm
}

// unquote strips one pair of matching YAML quotes, or a trailing comment from
// an unquoted value.
func unquote(v string) string {
	if len(v) >= 2 {
		switch {
		case v[0] == '"' && v[len(v)-1] == '"':
			return strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`)
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

// Candidate is a skill folder found in a repo: its slash path relative to the
// repo root ("." for the root itself) and the name from its SKILL.md.
type Candidate struct {
	Path string
	Name string
}

// Match picks the folder that holds the skill with the given slug. skills.sh
// derives a skillId from the SKILL.md name (and does not list a skill without
// one), so only the name decides a match. A folder name alone never does:
// otherwise a skill could be installed under a slug skills.sh never audited.
// Among several matches, a folder whose name also matches wins, then the
// shallowest, then the lexically first path, so the choice is deterministic.
func Match(slug string, cands []Candidate) (Candidate, bool) {
	var hits []Candidate
	for _, c := range cands {
		if c.Name != "" && Slug(c.Name) == slug {
			hits = append(hits, c)
		}
	}
	if len(hits) == 0 {
		return Candidate{}, false
	}
	folder := func(c Candidate) bool { return c.Path != "." && Slug(path.Base(c.Path)) == slug }
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if fa, fb := folder(a), folder(b); fa != fb {
			return fa
		}
		if da, db := depth(a.Path), depth(b.Path); da != db {
			return da < db
		}
		return a.Path < b.Path
	})
	return hits[0], true
}

func depth(p string) int {
	if p == "." {
		return 0
	}
	return strings.Count(p, "/") + 1
}
