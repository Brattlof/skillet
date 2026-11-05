package skillssh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestParseFrontmatter(t *testing.T) {
	cases := []struct {
		name, doc string
		want      Frontmatter
	}{
		{"plain", "---\nname: pdf\ndescription: Work with PDFs.\n---\nbody", Frontmatter{"pdf", "Work with PDFs."}},
		{"crlf and bom", "\ufeff---\r\nname: pdf\r\ndescription: x\r\n---\r\n", Frontmatter{"pdf", "x"}},
		{"double quoted", "---\nname: \"pdf\"\ndescription: \"Say \\\"hi\\\"\"\n---", Frontmatter{"pdf", `Say "hi"`}},
		{"single quoted", "---\nname: 'it''s'\n---", Frontmatter{"it's", ""}},
		{"folded block", "---\nname: x\ndescription: >-\n  first line\n  second line\nlicense: MIT\n---", Frontmatter{"x", "first line second line"}},
		{"literal block", "---\ndescription: |\n  a\n\n  b\nname: y\n---", Frontmatter{"y", "a b"}},
		{"nested keys ignored", "---\nmetadata:\n  name: inner\nname: outer\n---", Frontmatter{"outer", ""}},
		{"trailing comment", "---\nname: pdf # the name\n---", Frontmatter{"pdf", ""}},
		{"comment lines", "---\n# name: commented\nname: real\n---", Frontmatter{"real", ""}},
		{"no header", "# Title\nname: not-frontmatter", Frontmatter{}},
		{"empty", "", Frontmatter{}},
		{"unterminated", "---\nname: x\ndescription: y", Frontmatter{"x", "y"}},
		{"stops at closing fence", "---\nname: x\n---\nname: body-name\n", Frontmatter{"x", ""}},
		{"control chars cleaned", "---\nname: \x1b[31mred\x1b[0m\ndescription: a\u202eb\n---", Frontmatter{"[31mred[0m", "ab"}},
		{"literal greater-than", "---\nname: s\ndescription: \">\"\n---", Frontmatter{"s", ">"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseFrontmatter(tc.doc); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseFrontmatterBoundsInput(t *testing.T) {
	doc := "---\ndescription: " + strings.Repeat("x", 10*maxFrontmatter) + "\nname: late\n---"
	fm := ParseFrontmatter(doc)
	if fm.Name != "" {
		t.Fatal("a name past the scan limit must not be read")
	}
	if len(fm.Description) > maxFrontmatter {
		t.Fatalf("description longer than the scan limit: %d", len(fm.Description))
	}
}

func TestMatch(t *testing.T) {
	cands := []Candidate{
		{Path: "skills/other", Name: "PDF Tools"},   // name slug pdf-tools
		{Path: "plugins/x/skills/pdf", Name: "pdf"}, // name and folder, deeper
		{Path: "skills/pdf", Name: "pdf"},           // name and folder, shallower: wins
		{Path: "legacy/pdf", Name: ""},              // folder only: never matches
		{Path: "skills/renamed", Name: "pdf"},       // name only
	}
	got, ok := Match("pdf", cands)
	if !ok || got.Path != "skills/pdf" {
		t.Fatalf("got %+v", got)
	}
	// Without a folder match, a name match still wins.
	got, _ = Match("pdf", []Candidate{{Path: "legacy/pdf"}, {Path: "skills/renamed", Name: "pdf"}})
	if got.Path != "skills/renamed" {
		t.Fatalf("name match should win, got %+v", got)
	}
	// A folder name alone never matches, even with no other candidate, and
	// even when its SKILL.md names a different skill: the audit is keyed by name.
	if got, ok := Match("pdf", []Candidate{{Path: "legacy/pdf"}}); ok {
		t.Fatalf("a nameless SKILL.md matched by folder: %+v", got)
	}
	if got, ok := Match("evil", []Candidate{{Path: "skills/evil", Name: "evil-skill"}}); ok {
		t.Fatalf("a folder matched despite a different SKILL.md name: %+v", got)
	}
	// The repo root matches by name.
	got, ok = Match("root-skill", []Candidate{{Path: ".", Name: "Root Skill"}})
	if !ok || got.Path != "." {
		t.Fatalf("root match failed: %+v", got)
	}
	if _, ok := Match("missing", cands); ok {
		t.Fatal("no candidate should match")
	}
	// Equal rank and depth resolve lexically, independent of input order.
	a, _ := Match("x", []Candidate{{Path: "b/x", Name: "x"}, {Path: "a/x", Name: "x"}})
	b, _ := Match("x", []Candidate{{Path: "a/x", Name: "x"}, {Path: "b/x", Name: "x"}})
	if a.Path != "a/x" || b.Path != "a/x" {
		t.Fatalf("tie-break not deterministic: %q %q", a.Path, b.Path)
	}
}

// page renders rows the way skills.sh embeds them: a JSON payload split across
// several self.__next_f.push chunks, each a JSON string literal.
func page(t *testing.T, payload string, splits int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<html><script>self.__next_f.push([0])</script>`)
	step := len(payload)/splits + 1
	for i := 0; i < len(payload); i += step {
		chunk, _ := json.Marshal(payload[i:min(i+step, len(payload))])
		fmt.Fprintf(&b, `<script>self.__next_f.push([1,%s])</script>`, chunk)
	}
	b.WriteString(`<script>self.__next_f.push([2,"ignored"])</script></html>`)
	return b.String()
}

func TestParseLeaderboard(t *testing.T) {
	rows := `[{"source":"a/b","skillId":"one","name":"one","installs":5,"isOfficial":true},` +
		`{"source":"c/d","skillId":"two","name":"two","installs":3,"installsYesterday":1,"change":2}]`
	payload := `56:["$","$L64",null,{"initialSkills":[{"source":"decoy/x","skillId":"d","name":"d","installs":1}]}]` +
		"\n57:" + `{"initialSkills":` + rows + `,"other":1}`
	got, err := ParseLeaderboard([]byte(page(t, payload, 7)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID() != "a/b/one" || !got[0].Official || got[1].Change != 2 {
		t.Fatalf("parsed %+v", got)
	}
}

func TestParseLeaderboardFailsLoudly(t *testing.T) {
	for _, p := range []string{
		"",
		"<html>no payload</html>",
		page(t, `{"somethingElse":[1,2]}`, 2),
		page(t, `{"initialSkills":"not an array"}`, 1),
		page(t, `{"initialSkills":[]}`, 1),
	} {
		if _, err := ParseLeaderboard([]byte(p)); err == nil {
			t.Errorf("expected an error for %.60q", p)
		}
	}
}

func TestLeaderboardDropsUninstallable(t *testing.T) {
	rows := `{"initialSkills":[{"source":"a/b","skillId":"ok","name":"ok","installs":1},` +
		`{"source":"open.feishu.cn","skillId":"web","name":"web","installs":1},` +
		`{"source":"a/b","skillId":"../escape","name":"x","installs":1}]}`
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hot" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, page(t, rows, 3))
	})
	got, err := c.Leaderboard(context.Background(), ViewHot)
	if err != nil || len(got) != 1 || got[0].SkillID != "ok" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := c.Leaderboard(context.Background(), "../admin"); err == nil {
		t.Fatal("an unknown view must be rejected")
	}
}

func FuzzParseFrontmatter(f *testing.F) {
	for _, s := range []string{"---\nname: x\n---", "---\ndescription: >\n  a\n---", "\ufeff---\r\nname: 'a''b'\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		fm := ParseFrontmatter(doc)
		for _, s := range []string{fm.Name, fm.Description} {
			for _, r := range s {
				if unicode.IsControl(r) {
					t.Fatalf("control rune %U survived in %q", r, s)
				}
			}
		}
	})
}

func FuzzParseLeaderboard(f *testing.F) {
	f.Add([]byte(`self.__next_f.push([1,"{\"initialSkills\":[{\"source\":\"a/b\",\"skillId\":\"c\"}]}"])`))
	f.Add([]byte(`self.__next_f.push([1,"\"initialSkills\":["])`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseLeaderboard(b) // must not panic or hang
	})
}

// Nested arrays once made each attempt rescan the payload, taking seconds for a
// few hundred KB. Parsing must stay roughly linear.
func TestParseLeaderboardNestedIsFast(t *testing.T) {
	var b strings.Builder
	const n = 3000 // 9000 levels of JSON nesting, under the decoder's depth limit
	for i := 0; i < n; i++ {
		b.WriteString(`{"initialSkills":[{"source":"a/b","skillId":"c","x":`)
	}
	b.WriteString(`1`)
	for i := 0; i < n; i++ {
		b.WriteString(`}]}`)
	}
	start := time.Now()
	rows, err := ParseLeaderboard([]byte(page(t, b.String(), 1)))
	if err != nil || len(rows) != 1 {
		t.Fatalf("got %d rows, %v", len(rows), err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("nested payload took %v", el)
	}
}
