package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brattlof/skillet/internal/install"
	"github.com/Brattlof/skillet/internal/skillssh"
	"github.com/Brattlof/skillet/internal/trending"
)

// fakeGitHub makes "https://github.com/<owner>/<repo>" clone from local repos, so
// the community install path runs end to end without the network. It uses git's
// url.<base>.insteadOf rewrite through the environment.
type fakeGitHub struct{ base string }

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+base+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/")
	return &fakeGitHub{base: base}
}

// repo creates or updates owner/repo with files and commits. A nil value deletes.
func (g *fakeGitHub) repo(t *testing.T, source string, files map[string]*string) {
	t.Helper()
	dir := filepath.Join(g.base, filepath.FromSlash(source))
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		git("init", "-q")
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if content == nil {
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(*content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-qm", "change", "--allow-empty")
}

func str(s string) *string { return &s }

func skillDoc(name string) *string { return str("---\nname: " + name + "\ndescription: d\n---\n") }

// fakeSkillsSH serves search and audit results and counts requests.
type fakeSkillsSH struct {
	hits   atomic.Int32
	audits map[string]string // "source/skill" -> verdict; missing = not audited
	search string            // raw /api/search reply
	status int               // non-zero: every request returns it
}

func (f *fakeSkillsSH) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/skills/audit/"); ok {
			status, audited := f.audits[id]
			if !audited {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"audits": []map[string]string{{"provider": "Socket", "status": status, "summary": "why"}}})
			return
		}
		switch r.URL.Path {
		case "/api/search":
			fmt.Fprint(w, f.search)
		case "/api/download/acme/tools/lint":
			fmt.Fprint(w, `{"files":[{"path":"SKILL.md","contents":"---\nname: lint\ndescription: Lints \u001b[31mcode\n---"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SKILLET_SKILLSSH_URL", srv.URL)
}

// isolate points every skillet path at temp dirs and the registry at nothing,
// so the embedded baseline is used.
func isolate(t *testing.T) (skillsDir string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SKILLET_CACHE_DIR", t.TempDir())
	t.Setenv("SKILLET_LOCKFILE", filepath.Join(t.TempDir(), "skillet.lock"))
	t.Setenv("SKILLET_REGISTRY_URL", "http://127.0.0.1:0/index.json")
	t.Setenv("SKILLET_OFFLINE", "")
	return t.TempDir()
}

type result struct {
	code           int
	stdout, stderr string
}

func run(t *testing.T, args ...string) result {
	t.Helper()
	var r result
	r.stderr = captureStderr(t, func() {
		r.stdout = captureStdout(t, func() { r.code = Run(context.Background(), args) })
	})
	return r
}

func TestCommunityInstallLifecycle(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	sk := &fakeSkillsSH{audits: map[string]string{"acme/tools/lint": "pass", "acme/tools/danger": "fail"}}
	sk.start(t)
	gh.repo(t, "acme/tools", map[string]*string{
		"skills/lint/SKILL.md":   skillDoc("lint"),
		"skills/lint/rules.md":   str("v1"),
		"skills/danger/SKILL.md": skillDoc("danger"),
	})

	// Install: the audit is shown, the folder is found, and the lock records the source.
	r := run(t, "add", "acme/tools/lint", "--dir", dir)
	if r.code != 0 {
		t.Fatalf("add failed: %+v", r)
	}
	if !strings.Contains(r.stderr, "audit: Socket pass") || !strings.Contains(r.stderr, "community skill from skills.sh") {
		t.Errorf("missing audit or community warning:\n%s", r.stderr)
	}
	if !strings.Contains(r.stdout, "(from skills/lint)") {
		t.Errorf("install output should name the folder:\n%s", r.stdout)
	}
	rec, ok, _ := install.ReadRecord(dir, "lint")
	if !ok || rec.Source != skillssh.SourceName || rec.Path != "skills/lint" || rec.Repo != "https://github.com/acme/tools" {
		t.Fatalf("record = %+v", rec)
	}
	lf, _ := install.ReadLock(os.Getenv("SKILLET_LOCKFILE"))
	if len(lf.Skills) != 1 || lf.Skills[0].Source != skillssh.SourceName {
		t.Fatalf("lock = %+v", lf)
	}

	// A critical audit blocks the install before anything is cloned.
	r = run(t, "add", "acme/tools/danger", "--dir", dir)
	if r.code == 0 || !strings.Contains(r.stderr, "fails its skills.sh security audit") || !strings.Contains(r.stderr, "--force") {
		t.Fatalf("critical audit should block: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "danger")); !os.IsNotExist(err) {
		t.Fatal("a blocked skill was installed")
	}
	r = run(t, "add", "acme/tools/danger", "--dir", dir, "--force")
	if r.code != 0 || !strings.Contains(r.stderr, "despite a failed security audit") {
		t.Fatalf("--force should install with a warning: %+v", r)
	}

	// list and doctor treat community installs as their own category.
	if r := run(t, "list", "--dir", dir); !strings.Contains(r.stdout, "community") {
		t.Errorf("list should mark community installs:\n%s", r.stdout)
	}
	if r := run(t, "doctor", "--dir", dir); r.code != 0 || !strings.Contains(r.stdout, "lint (community, from skills.sh)") {
		t.Errorf("doctor: %+v", r)
	}

	// The skill moves upstream: update follows it to the new folder.
	gh.repo(t, "acme/tools", map[string]*string{
		"skills/lint":                  nil,
		"skills/quality/lint/SKILL.md": skillDoc("lint"),
		"skills/quality/lint/rules.md": str("v2"),
	})
	r = run(t, "update", "lint", "--dir", dir)
	if r.code != 0 || !strings.Contains(r.stdout, "updated lint") {
		t.Fatalf("update: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "lint", "rules.md")); string(b) != "v2" {
		t.Fatalf("update did not install the moved skill, rules.md = %q", b)
	}
	if rec, _, _ := install.ReadRecord(dir, "lint"); rec.Path != "skills/quality/lint" || rec.Source != skillssh.SourceName {
		t.Fatalf("record after update = %+v", rec)
	}

	// Restore from the lockfile keeps the source, so update still works after.
	if r := run(t, "lock", "--dir", dir); r.code != 0 {
		t.Fatalf("lock: %+v", r)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// Restore re-audits community entries: the critical one is skipped until --force.
	r = run(t, "install", "--dir", dir)
	if r.code != 0 || !strings.Contains(r.stderr, "skipping danger") || !strings.Contains(r.stdout, "skipped 1") {
		t.Fatalf("restore should skip the critical skill: %+v", r)
	}
	if rec, ok, _ := install.ReadRecord(dir, "lint"); !ok || rec.Source != skillssh.SourceName {
		t.Fatalf("restored record lost its source: %+v", rec)
	}
	if r := run(t, "install", "--dir", dir, "--force"); r.code != 0 {
		t.Fatalf("restore --force: %+v", r)
	}
	if r := run(t, "install", "--frozen", "--dir", dir); r.code != 0 {
		t.Fatalf("frozen verify after restore: %+v", r)
	}
}

// The audit keys on the SKILL.md name. Asking for a folder name whose SKILL.md
// names a different (critical) skill must not install it under the folder name.
func TestCommunityFolderNameCannotDodgeAudit(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	(&fakeSkillsSH{audits: map[string]string{"evil/repo/evil-skill": "fail"}}).start(t)
	gh.repo(t, "evil/repo", map[string]*string{"skills/evil/SKILL.md": skillDoc("evil-skill")})
	if r := run(t, "add", "evil/repo/evil-skill", "--dir", dir); r.code == 0 {
		t.Fatal("the critical skill should be blocked")
	}
	r := run(t, "add", "evil/repo/evil", "--dir", dir)
	if r.code == 0 || !strings.Contains(r.stderr, `no skill named "evil"`) {
		t.Fatalf("installing by folder name must fail: %+v", r)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files were installed: %v", entries)
	}
}

// A skill that turns critical after install is not updated without --force.
func TestCommunityUpdateReaudits(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	sk := &fakeSkillsSH{audits: map[string]string{"rug/repo/good": "pass"}}
	sk.start(t)
	gh.repo(t, "rug/repo", map[string]*string{"skills/good/SKILL.md": skillDoc("good"), "skills/good/x.md": str("v1")})
	if r := run(t, "add", "rug/repo/good", "--dir", dir); r.code != 0 {
		t.Fatalf("add: %+v", r)
	}
	gh.repo(t, "rug/repo", map[string]*string{"skills/good/x.md": str("v2 malicious")})
	sk.audits["rug/repo/good"] = "fail"

	r := run(t, "update", "--dir", dir)
	if !strings.Contains(r.stdout, "skipped good (blocked by its security audit)") || !strings.Contains(r.stderr, "fails its skills.sh security audit") {
		t.Fatalf("update should be blocked: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "good", "x.md")); string(b) != "v1" {
		t.Fatalf("blocked update changed the skill: %q", b)
	}
	if r := run(t, "update", "--dir", dir, "--force"); r.code != 0 || !strings.Contains(r.stdout, "updated good") {
		t.Fatalf("update --force: %+v", r)
	}
}

// A lockfile entry marked skills.sh may not point its audited name at another
// folder: restore finds the folder by name and ignores the recorded path.
func TestRestoreIgnoresLockPathForCommunity(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	(&fakeSkillsSH{audits: map[string]string{"acme/tools/good": "pass", "acme/tools/evil": "fail"}}).start(t)
	gh.repo(t, "acme/tools", map[string]*string{
		"skills/good/SKILL.md": skillDoc("good"), "skills/good/x.md": str("good"),
		"skills/evil/SKILL.md": skillDoc("evil"), "skills/evil/x.md": str("evil"),
	})
	if err := install.WriteLock(os.Getenv("SKILLET_LOCKFILE"), install.Lockfile{Skills: []install.LockEntry{
		{Name: "good", Kind: "skill", Repo: "https://github.com/acme/tools", Path: "skills/evil", Source: skillssh.SourceName},
	}}); err != nil {
		t.Fatal(err)
	}
	if r := run(t, "install", "--dir", dir); r.code != 0 {
		t.Fatalf("restore: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "good", "x.md")); string(b) != "good" {
		t.Fatalf("restore installed the folder the lockfile pointed at: %q", b)
	}
}

// A folder skillet did not install is never deleted without --force.
func TestAddRefusesToOverwriteUnmanagedFolder(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	(&fakeSkillsSH{}).start(t)
	gh.repo(t, "acme/tools", map[string]*string{"skills/lint/SKILL.md": skillDoc("lint")})
	mine := filepath.Join(dir, "lint", "NOTES.md")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("my work"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := run(t, "add", "acme/tools/lint", "--dir", dir)
	if r.code == 0 || !strings.Contains(r.stderr, "not installed by skillet") || !strings.Contains(r.stderr, "--replace") {
		t.Fatalf("add should refuse: %+v", r)
	}
	// --force is about the audit only; it must not delete the folder.
	if r := run(t, "add", "acme/tools/lint", "--dir", dir, "--force"); r.code == 0 {
		t.Fatalf("--force replaced an unmanaged folder: %+v", r)
	}
	if b, _ := os.ReadFile(mine); string(b) != "my work" {
		t.Fatal("a hand-made folder was deleted")
	}
	if r := run(t, "add", "acme/tools/lint", "--dir", dir, "--replace"); r.code != 0 {
		t.Fatalf("add --replace: %+v", r)
	}
	// Once skillet owns it, reinstalling needs no --force.
	if r := run(t, "add", "acme/tools/lint", "--dir", dir); r.code != 0 {
		t.Fatalf("reinstall: %+v", r)
	}
}

func TestCommunityUpdateRejectsTamperedRecord(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	(&fakeSkillsSH{}).start(t)
	gh.repo(t, "acme/tools", map[string]*string{"skills/lint/SKILL.md": skillDoc("lint")})
	if r := run(t, "add", "acme/tools/lint", "--dir", dir); r.code != 0 {
		t.Fatalf("add: %+v", r)
	}
	meta := filepath.Join(dir, ".skillet", "lint.json")
	orig, err := os.ReadFile(meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, tamper := range []struct{ old, new string }{
		{`"repo": "https://github.com/acme/tools"`, `"repo": "https://evil.example/acme/tools"`},
		{`"repo": "https://github.com/acme/tools"`, `"repo": "https://github.com/../../etc"`},
		{`"repo": "https://github.com/acme/tools"`, `"repo": "https://github.com/acme/tools", "ref": "--upload-pack=touch /tmp/pwned"`},
	} {
		if err := os.WriteFile(meta, []byte(strings.Replace(string(orig), tamper.old, tamper.new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		r := run(t, "update", "--dir", dir)
		if r.code != 0 || !strings.Contains(r.stdout, "skipped lint (its install record") {
			t.Fatalf("tampered record (%s) should be skipped: %+v", tamper.new, r)
		}
	}
}

func TestRestoreHonorsOnlyKnownSource(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	gh.repo(t, "acme/tools", map[string]*string{"skills/lint/SKILL.md": skillDoc("lint")})
	lock := os.Getenv("SKILLET_LOCKFILE")
	if err := install.WriteLock(lock, install.Lockfile{Skills: []install.LockEntry{
		{Name: "lint", Kind: "skill", Repo: "https://github.com/acme/tools", Path: "skills/lint", Source: "something-else"},
	}}); err != nil {
		t.Fatal(err)
	}
	if r := run(t, "install", "--dir", dir); r.code != 0 {
		t.Fatalf("restore: %+v", r)
	}
	if rec, _, _ := install.ReadRecord(dir, "lint"); rec.Source != "" {
		t.Fatalf("an unknown source must not be recorded: %q", rec.Source)
	}
}

func TestAddRejectsUnsafeSpecsWithoutNetwork(t *testing.T) {
	dir := isolate(t)
	sk := &fakeSkillsSH{}
	sk.start(t)
	for _, spec := range []string{
		"acme/tools/../../etc", "acme/tools/.git", "acme/tools/-x", "-o/tools/x", "acme/../x",
		"uizze.sh/ui-taste", "acme/tools", "acme/tools/x/y",
	} {
		if r := run(t, "add", spec, "--dir", dir); r.code == 0 {
			t.Errorf("add %q should fail", spec)
		}
	}
	if r := run(t, "add", "acme/tools/x@--upload-pack=touch", "--dir", dir); r.code == 0 || !strings.Contains(r.stderr, "invalid ref") {
		t.Errorf("an option-like ref must be rejected: %+v", r)
	}
	if n := sk.hits.Load(); n != 0 {
		t.Errorf("rejected specs reached skills.sh %d times", n)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("rejected specs left files: %v", entries)
	}
}

func TestSearchShowsCommunityResults(t *testing.T) {
	isolate(t)
	// The embedded registry has hello-skill from Brattlof/skillet; a skills.sh
	// copy of it must be deduplicated, everything invalid dropped.
	sk := &fakeSkillsSH{search: `{"skills":[
		{"source":"Brattlof/skillet","skillId":"hello-skill","name":"hello-skill","installs":5},
		{"source":"other/repo","skillId":"hello-world","name":"hello-world","installs":1234},
		{"source":"uizze.sh","skillId":"hello-web","name":"x","installs":1},
		{"source":"../../x","skillId":"hello","name":"x","installs":1}
	]}`}
	sk.start(t)

	r := run(t, "search", "hello")
	if r.code != 0 {
		t.Fatalf("search: %+v", r)
	}
	if !strings.Contains(r.stdout, "hello-skill") || !strings.Contains(r.stdout, "From skills.sh") || !strings.Contains(r.stdout, "other/repo/hello-world  1.2k") {
		t.Fatalf("unexpected output:\n%s", r.stdout)
	}
	for _, bad := range []string{"Brattlof/skillet/hello-skill", "uizze.sh", "../"} {
		if strings.Contains(r.stdout, bad) {
			t.Errorf("output should not contain %q:\n%s", bad, r.stdout)
		}
	}

	// Filters and offline mode keep search local.
	before := sk.hits.Load()
	run(t, "search", "hello", "--kind", "mcp")
	run(t, "search", "hello", "--tag", "example")
	run(t, "search")
	t.Setenv("SKILLET_OFFLINE", "1")
	run(t, "search", "hello")
	if sk.hits.Load() != before {
		t.Errorf("filtered, empty, or offline search reached skills.sh")
	}
}

func TestSearchSurvivesSkillsSHOutage(t *testing.T) {
	isolate(t)
	(&fakeSkillsSH{status: 503}).start(t)
	r := run(t, "search", "hello")
	if r.code != 0 || !strings.Contains(r.stdout, "hello-skill") || !strings.Contains(r.stderr, "skills.sh search unavailable") {
		t.Fatalf("registry results must survive a skills.sh outage: %+v", r)
	}
}

func TestInfoCommunity(t *testing.T) {
	dir := isolate(t)
	(&fakeSkillsSH{audits: map[string]string{"acme/tools/lint": "warn"}}).start(t)
	r := run(t, "info", "acme/tools/lint", "--dir", dir)
	if r.code != 0 {
		t.Fatalf("info: %+v", r)
	}
	for _, want := range []string{"Description  Lints [31mcode", "Audit        Socket: warn - why", "skillet add acme/tools/lint", "Installed    no"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("info missing %q:\n%s", want, r.stdout)
		}
	}
	if strings.ContainsRune(r.stdout, 0x1b) {
		t.Error("an escape character reached the terminal")
	}
}

func serveSnapshot(t *testing.T, s trending.Snapshot) {
	t.Helper()
	b, _ := json.Marshal(s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(b) }))
	t.Cleanup(srv.Close)
	t.Setenv("SKILLET_TRENDING_URL", srv.URL)
}

func TestTrendingAndHot(t *testing.T) {
	isolate(t)
	snap := trending.Snapshot{GeneratedAt: time.Now().Add(-10 * time.Minute)}
	for i := 0; i < 30; i++ {
		snap.Trending = append(snap.Trending, trending.Item{Source: "acme/tools", Skill: fmt.Sprintf("s%d", i), Name: "s", Installs: int64(20000 - i), Stars: 99, Audit: "low", Description: "Does \u001b[2Jthings"})
	}
	snap.Hot = []trending.Item{{Source: "acme/tools", Skill: "rising", Installs: 120, Change: 80, Stars: 99}}
	serveSnapshot(t, snap)

	r := run(t, "trending")
	if r.code != 0 {
		t.Fatalf("trending: %+v", r)
	}
	if !strings.Contains(r.stdout, "updated 10 min ago") || !strings.Contains(r.stdout, "acme/tools/s0") || !strings.Contains(r.stdout, "20.0k") {
		t.Fatalf("unexpected output:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "acme/tools/s20") {
		t.Error("default limit is 20")
	}
	if strings.ContainsRune(r.stdout, 0x1b) {
		t.Error("an escape character reached the terminal")
	}
	if r := run(t, "trending", "--limit", "3"); strings.Contains(r.stdout, "acme/tools/s3 ") {
		t.Errorf("--limit 3 showed a 4th row:\n%s", r.stdout)
	}
	if r := run(t, "trending", "--limit", "0"); r.code == 0 {
		t.Error("--limit 0 should be rejected")
	}
	r = run(t, "hot")
	if r.code != 0 || !strings.Contains(r.stdout, "acme/tools/rising") || !strings.Contains(r.stdout, "120 (+80)") {
		t.Fatalf("hot: %+v", r)
	}
}

func TestTrendingStaleAndUnavailable(t *testing.T) {
	isolate(t)
	serveSnapshot(t, trending.Snapshot{GeneratedAt: time.Now().Add(-30 * time.Hour), Trending: []trending.Item{{Source: "a/b", Skill: "c", Name: "c"}}})
	if r := run(t, "trending"); r.code != 0 || !strings.Contains(r.stderr, "may be failing") {
		t.Fatalf("a stale snapshot should warn: %+v", r)
	}

	isolate(t)
	t.Setenv("SKILLET_TRENDING_URL", "http://127.0.0.1:0/trending.json")
	if r := run(t, "trending"); r.code == 0 || !strings.Contains(r.stderr, "unavailable") {
		t.Fatalf("no snapshot should fail clearly: %+v", r)
	}

	// A tampered snapshot is rejected as a whole.
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"generated_at":%q,"trending":[{"source":"../../etc","skill":"passwd"}],"hot":[]}`, time.Now().Format(time.RFC3339))
	}))
	defer srv.Close()
	t.Setenv("SKILLET_TRENDING_URL", srv.URL)
	if r := run(t, "trending"); r.code == 0 {
		t.Fatalf("a tampered snapshot must be rejected: %+v", r)
	}
}

// addCommunity re-checks its inputs instead of trusting the caller's parse.
func TestAddCommunityValidatesDirectly(t *testing.T) {
	dir := isolate(t)
	sk := &fakeSkillsSH{}
	sk.start(t)
	for _, c := range [][2]string{{"../x", "lint"}, {"acme/tools", "../lint"}, {"-u/x", "lint"}, {"uizze.sh", "lint"}} {
		var err error
		captureStderr(t, func() {
			captureStdout(t, func() { err = addCommunity(context.Background(), c[0], c[1], "", "", dir, false, false) })
		})
		if err == nil || !strings.Contains(err.Error(), "invalid community skill") {
			t.Errorf("addCommunity(%q, %q) = %v", c[0], c[1], err)
		}
	}
	if sk.hits.Load() != 0 {
		t.Error("invalid input reached skills.sh")
	}
}

// --replace is about the folder only; it must not bypass a blocking audit.
func TestReplaceDoesNotBypassAudit(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	(&fakeSkillsSH{audits: map[string]string{"acme/tools/danger": "fail"}}).start(t)
	gh.repo(t, "acme/tools", map[string]*string{"skills/danger/SKILL.md": skillDoc("danger")})
	if r := run(t, "add", "acme/tools/danger", "--dir", dir, "--replace"); r.code == 0 {
		t.Fatalf("--replace bypassed a critical audit: %+v", r)
	}
	lock := os.Getenv("SKILLET_LOCKFILE")
	if err := install.WriteLock(lock, install.Lockfile{Skills: []install.LockEntry{
		{Name: "danger", Kind: "skill", Repo: "https://github.com/acme/tools", Path: "skills/danger", Source: skillssh.SourceName},
	}}); err != nil {
		t.Fatal(err)
	}
	if r := run(t, "install", "--dir", dir, "--replace"); !strings.Contains(r.stderr, "skipping danger") {
		t.Fatalf("restore --replace bypassed a critical audit: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "danger")); !os.IsNotExist(err) {
		t.Fatal("the critical skill was installed")
	}
}

// The audit covers a skill's current version only, so a pinned ref needs --force.
func TestCommunityRefNeedsForce(t *testing.T) {
	dir := isolate(t)
	gh := newFakeGitHub(t)
	sk := &fakeSkillsSH{audits: map[string]string{"rug/repo/good": "pass"}}
	sk.start(t)
	gh.repo(t, "rug/repo", map[string]*string{"skills/good/SKILL.md": skillDoc("good"), "skills/good/x.md": str("old")})
	tag := exec.Command("git", "tag", "bad")
	tag.Dir = filepath.Join(gh.base, "rug", "repo")
	if out, err := tag.CombinedOutput(); err != nil {
		t.Fatalf("git tag: %v %s", err, out)
	}
	gh.repo(t, "rug/repo", map[string]*string{"skills/good/x.md": str("new")})
	r := run(t, "add", "rug/repo/good@bad", "--dir", dir)
	if r.code == 0 || !strings.Contains(r.stderr, "covers the current version") {
		t.Fatalf("a pinned ref should need --force: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "good")); !os.IsNotExist(err) {
		t.Fatal("installed without --force")
	}
	if n := sk.hits.Load(); n != 0 {
		t.Errorf("blocked before the audit, but made %d requests", n)
	}
	r = run(t, "add", "rug/repo/good@bad", "--dir", dir, "--force")
	if r.code != 0 || !strings.Contains(r.stderr, "not bad (--force)") {
		t.Fatalf("--force should install the pinned ref with a warning: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "good", "x.md")); string(b) != "old" {
		t.Fatalf("pinned ref not installed: %q", b)
	}
}
