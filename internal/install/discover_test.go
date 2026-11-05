package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Brattlof/skillet/internal/registry"
	"github.com/Brattlof/skillet/internal/skillssh"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func skillMD(name string) string { return "---\nname: " + name + "\ndescription: d\n---\n" }

func TestFindSkill(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"skills/pdf/SKILL.md":                    skillMD("pdf"),
		"skills/productivity/grill-me/SKILL.md":  skillMD("grill-me"),
		"skills/renamed-folder/SKILL.md":         skillMD("Fancy Name"),
		"node_modules/pkg/skills/evil/SKILL.md":  skillMD("evil"),
		"dist/copy/SKILL.md":                     skillMD("dist-copy"),
		"a/b/c/d/e/f/g/h/i/too-deep/SKILL.md":    skillMD("too-deep"),
		"lowercase/skill.md":                     skillMD("lower"),
		".claude/skills/hidden-but-ok/SKILL.md":  skillMD("hidden-but-ok"),
		"skills/no-frontmatter/SKILL.md":         "# just a title\n",
		"skills/pdf/nested/inner-pdf/SKILL.md":   skillMD("inner"),
		"docs/not-a-skill/README.md":             "x",
		"skills/grill-me-legacy/SKILL.md":        skillMD("grill-me-legacy"),
		"plugins/x/skills/grill-me/SKILL.md":     skillMD("grill-me"),
		"skills/productivity/grill-me/extra.txt": "x",
	})
	cases := map[string]string{
		"pdf":           "skills/pdf",
		"grill-me":      "skills/productivity/grill-me", // shallower of two both-matches
		"fancy-name":    "skills/renamed-folder",        // by frontmatter name
		"lower":         "lowercase",
		"hidden-but-ok": ".claude/skills/hidden-but-ok",
	}
	for slug, want := range cases {
		got, err := findSkill(root, slug)
		if err != nil || got != want {
			t.Errorf("findSkill(%q) = %q, %v; want %q", slug, got, err, want)
		}
	}
	// no-frontmatter: a folder name alone never matches (see skillssh.Match).
	for _, slug := range []string{"evil", "dist-copy", "too-deep", "missing", "no-frontmatter"} {
		if got, err := findSkill(root, slug); err == nil {
			t.Errorf("findSkill(%q) = %q, want an error", slug, got)
		}
	}
}

func TestFindSkillAtRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"SKILL.md": skillMD("Root Skill"), "scripts/run.sh": "x"})
	got, err := findSkill(root, "root-skill")
	if err != nil || got != "." {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A repo must not be able to point discovery at files outside the clone.
func TestFindSkillIgnoresSymlinks(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"secret/SKILL.md": skillMD("target"), "loose.md": skillMD("target")})

	root := t.TempDir()
	writeTree(t, root, map[string]string{"skills/real/SKILL.md": skillMD("real")})
	if err := os.MkdirAll(filepath.Join(root, "skills/linkfile"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, filepath.Join(outside, "loose.md"), filepath.Join(root, "skills/linkfile/SKILL.md"))
	mustSymlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "skills/target"))
	mustSymlink(t, root, filepath.Join(root, "skills/loop")) // a cycle must not hang the walk

	if got, err := findSkill(root, "target"); err == nil {
		t.Fatalf("a symlinked SKILL.md or folder was used: %q", got)
	}
	if got, err := findSkill(root, "real"); err != nil || got != "skills/real" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
}

func TestSkillCandidatesBounded(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for i := 0; i <= maxSkillFiles; i++ {
		files[filepath.Join("s", strings.Repeat("x", 1+i%7), "n"+strconv.Itoa(i), "SKILL.md")] = "---\nname: n\n---"
	}
	writeTree(t, root, files)
	if _, err := skillCandidates(root); err != errRepoTooLarge {
		t.Fatalf("want errRepoTooLarge, got %v", err)
	}
}

// commitAll commits every change in a repo made by gitRepoWith.
func commitAll(t *testing.T, repo string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "more"}} {
		c := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestInstallWithDiscover(t *testing.T) {
	repo := gitRepoWith(t, map[string]string{
		"skills/tools/pdf/SKILL.md":   skillMD("pdf"),
		"skills/tools/pdf/scripts.sh": "echo hi",
		"skills/other/SKILL.md":       skillMD("other"),
		"README.md":                   "x",
	})
	dir := t.TempDir()
	e := registry.Entry{Name: "pdf", Repo: repo}
	dest, err := InstallWith(context.Background(), e, dir, Options{Source: skillssh.SourceName, Discover: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts.sh")); err != nil {
		t.Fatalf("skill files not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); !os.IsNotExist(err) {
		t.Fatal("files outside the skill folder must not be installed")
	}
	rec, ok, err := ReadRecord(dir, "pdf")
	if err != nil || !ok {
		t.Fatalf("no record: %v", err)
	}
	if rec.Source != skillssh.SourceName || rec.Path != "skills/tools/pdf" || rec.Commit == "" || rec.Cksum == "" {
		t.Fatalf("record = %+v", rec)
	}

	// The recorded path reinstalls without discovery (lockfile restore).
	e.Path = rec.Path
	if _, err := InstallWith(context.Background(), e, t.TempDir(), Options{Source: skillssh.SourceName}); err != nil {
		t.Fatalf("restore by path: %v", err)
	}
}

func TestInstallWithDiscoverErrors(t *testing.T) {
	repo := gitRepoWith(t, map[string]string{"skills/a/SKILL.md": skillMD("a")})
	dir := t.TempDir()
	_, err := InstallWith(context.Background(), registry.Entry{Name: "missing", Repo: repo}, dir, Options{Discover: "missing"})
	if err == nil || !strings.Contains(err.Error(), `no skill named "missing"`) {
		t.Fatalf("err = %v", err)
	}
	_, err = InstallWith(context.Background(), registry.Entry{Name: "a", Repo: repo, Kind: "hook"}, dir, Options{Discover: "a"})
	if err == nil {
		t.Fatal("discovery must be refused for a non-skill kind")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a failed install left files behind: %v", entries)
	}
}

// A hostile repo commits a SKILL.md symlink and a skill folder with a symlink to
// a host file. Neither may pull outside content into the install.
func TestInstallWithDiscoverHostileRepo(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"secret.txt": "TOP SECRET", "SKILL.md": skillMD("bait")})

	src := gitRepoWith(t, map[string]string{"skills/good/SKILL.md": skillMD("good")})
	mustSymlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(src, "skills/good/leak.txt"))
	if err := os.MkdirAll(filepath.Join(src, "skills/bait"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, filepath.Join(outside, "SKILL.md"), filepath.Join(src, "skills/bait/SKILL.md"))
	commitAll(t, src)

	dir := t.TempDir()
	if _, err := InstallWith(context.Background(), registry.Entry{Name: "bait", Repo: src}, dir, Options{Discover: "bait"}); err == nil {
		t.Fatal("a skill found only through a symlinked SKILL.md must not install")
	}
	dest, err := InstallWith(context.Background(), registry.Entry{Name: "good", Repo: src}, dir, Options{Discover: "good"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	_ = filepath.WalkDir(dest, func(p string, d os.DirEntry, _ error) error {
		names = append(names, d.Name())
		if !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "TOP SECRET") {
				t.Fatalf("outside content leaked into %s", p)
			}
		}
		return nil
	})
	if slices.Contains(names, "leak.txt") {
		t.Fatal("a symlink was copied into the skill")
	}
}

func TestGitNeverPrompts(t *testing.T) {
	cmd := gitCommand(context.Background(), "version")
	if !slices.Contains(cmd.Env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("git must run with terminal prompts disabled")
	}
}

func TestFindSkillSkipsControlCharacterPaths(t *testing.T) {
	root := t.TempDir()
	evil := "x\x1b]0;PWNED\a\x1b[2J"
	if err := os.MkdirAll(filepath.Join(root, "skills", evil), 0o755); err != nil {
		t.Skip("filesystem rejects control characters:", err)
	}
	writeTree(t, root, map[string]string{"skills/" + evil + "/SKILL.md": skillMD("esc-skill")})
	if got, err := findSkill(root, "esc-skill"); err == nil {
		t.Fatalf("a path with control characters was used: %q", got)
	}
}

func TestInstallRefusesUnmanagedDestination(t *testing.T) {
	repo := gitRepoWith(t, map[string]string{"skills/a/SKILL.md": skillMD("a")})
	dir := t.TempDir()
	victim := t.TempDir()
	if err := os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, victim, filepath.Join(dir, "a")) // a pre-planted link at the destination
	e := registry.Entry{Name: "a", Repo: repo}
	if _, err := InstallWith(context.Background(), e, dir, Options{Discover: "a"}); err == nil {
		t.Fatal("an unmanaged destination must not be replaced without Replace")
	}
	if _, err := InstallWith(context.Background(), e, dir, Options{Discover: "a", Replace: true}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(victim, "keep.txt")); err != nil || string(b) != "keep" {
		t.Fatal("replacing a symlinked destination touched its target")
	}
}
