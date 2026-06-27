package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.0.0", "v1.0.1", true},
		{"1.0.1", "v1.0.1", false},
		{"v1.2.0", "v1.1.9", false},
		{"1.0.0", "v2.0.0", true},
		{"1.0.0", "v1.1.0-rc1", true},  // a higher core version wins even as a pre-release
		{"1.2.0-rc1", "v1.2.0", true},  // the GA release is newer than its own rc
		{"1.2.0", "v1.2.0-rc1", false}, // an rc is not newer than the GA it precedes
		{"1.2.0-rc1", "v1.2.0-rc2", true},
		{"v1.0.0", "1.0.0", false},
		{"1.0.0+build7", "1.0.0", false}, // build metadata does not affect precedence
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	got := AssetName()
	prefix := "skillet_" + runtime.GOOS + "_" + runtime.GOARCH
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("AssetName() = %q, want prefix %q", got, prefix)
	}
	if !strings.HasSuffix(got, ".tar.gz") && !strings.HasSuffix(got, ".zip") {
		t.Fatalf("AssetName() = %q, want a .tar.gz or .zip suffix", got)
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("a fake release archive")
	sum := sha256.Sum256(data)
	name := "skillet_test.tar.gz"
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\nffff  other.tar.gz\n")

	if err := verifyChecksum(data, name, sums); err != nil {
		t.Fatalf("matching checksum should verify: %v", err)
	}
	if err := verifyChecksum(data, "missing.tar.gz", sums); err == nil {
		t.Error("missing checksum entry should error")
	}
	if err := verifyChecksum([]byte("tampered"), name, sums); err == nil {
		t.Error("tampered data should fail the checksum")
	}
}

func TestExtractBinaryTarGz(t *testing.T) {
	want := []byte("the-real-binary-bytes")
	archive := buildTarGz(t, binaryName(), want)

	got, err := extractBinary(archive, "skillet_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz")
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("extracted %q, want %q", got, want)
	}
}

func TestExtractBinaryZip(t *testing.T) {
	want := []byte("the-real-binary-bytes")
	archive := buildZip(t, binaryName(), want)

	// The .zip suffix selects the zip branch regardless of the host platform.
	got, err := extractBinary(archive, "skillet_windows_amd64.zip")
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("extracted %q, want %q", got, want)
	}
}

func TestExtractBinaryMissing(t *testing.T) {
	archive := buildTarGz(t, "not-skillet", []byte("decoy only"))
	if _, err := extractBinary(archive, "skillet_linux_amd64.tar.gz"); err == nil {
		t.Fatal("extractBinary should fail when the archive has no skillet binary")
	}
}

func TestCheckViaHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","assets":[]}`))
	}))
	defer srv.Close()
	withAPI(t, srv.URL)

	tag, avail, err := Check(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.2.0" || !avail {
		t.Fatalf("Check older = (%q, %v), want (v1.2.0, true)", tag, avail)
	}

	_, avail, err = Check(context.Background(), "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if avail {
		t.Fatal("Check current should report no update")
	}
}

func TestDownloadNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := download(context.Background(), srv.URL); err == nil {
		t.Fatal("download should error on a non-200 response")
	}
}

// TestUpdateVerifiesBeforeTrusting is the security guarantee the package comment
// promises: bytes from the release archive must be checked against the release
// checksums before anything reads or installs them. The served archive is not even
// a valid tar.gz, so if the order were ever flipped to extract-then-verify the
// failure would be a decompression error (or, worse, a call into replace); with
// verify-first it must be a checksum mismatch and replace must never run.
func TestUpdateVerifiesBeforeTrusting(t *testing.T) {
	bogus := []byte("this is not a real archive")
	sums := checksumsFor(AssetName(), []byte("a completely different payload"))

	srv := releaseServer(t, releaseConfig{tag: "v9.9.9", archive: bogus, sums: sums, hasAsset: true, hasSums: true})
	defer srv.Close()
	withAPI(t, srv.URL+"/release")

	replaced := false
	stubReplace(t, func([]byte) error { replaced = true; return nil })

	_, updated, err := Update(context.Background(), "1.0.0")
	if err == nil {
		t.Fatal("Update must fail when the checksum does not match")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want a checksum-mismatch error proving verify ran before extract, got: %v", err)
	}
	if updated {
		t.Error("updated must be false on a checksum failure")
	}
	if replaced {
		t.Error("replace must never run when verification fails")
	}
}

func TestUpdateHappyPath(t *testing.T) {
	bin := []byte("the freshly built skillet binary")
	archive := buildPlatformArchive(t, bin)
	sums := checksumsFor(AssetName(), archive)

	srv := releaseServer(t, releaseConfig{tag: "v9.9.9", archive: archive, sums: sums, hasAsset: true, hasSums: true})
	defer srv.Close()
	withAPI(t, srv.URL+"/release")

	var installed []byte
	stubReplace(t, func(b []byte) error { installed = b; return nil })

	tag, updated, err := Update(context.Background(), "1.0.0")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if tag != "v9.9.9" || !updated {
		t.Fatalf("Update = (%q, %v), want (v9.9.9, true)", tag, updated)
	}
	if !bytes.Equal(installed, bin) {
		t.Fatalf("installed %q, want the extracted binary %q", installed, bin)
	}
}

func TestUpdateNotNewer(t *testing.T) {
	srv := releaseServer(t, releaseConfig{tag: "v1.0.0"})
	defer srv.Close()
	withAPI(t, srv.URL+"/release")

	stubReplace(t, func([]byte) error {
		t.Error("replace must not run when the binary is already current")
		return nil
	})

	tag, updated, err := Update(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.0.0" || updated {
		t.Fatalf("Update = (%q, %v), want (v1.0.0, false)", tag, updated)
	}
}

func TestUpdateNoPlatformAsset(t *testing.T) {
	// No asset is advertised, so Update returns at the asset lookup, before any download.
	srv := releaseServer(t, releaseConfig{tag: "v9.9.9"})
	defer srv.Close()
	withAPI(t, srv.URL+"/release")

	if _, _, err := Update(context.Background(), "1.0.0"); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("want a missing-asset error, got: %v", err)
	}
}

func TestUpdateMissingChecksums(t *testing.T) {
	// The asset exists but checksums.txt does not, so Update returns at the
	// checksums lookup, before the archive is fetched.
	srv := releaseServer(t, releaseConfig{tag: "v9.9.9", hasAsset: true})
	defer srv.Close()
	withAPI(t, srv.URL+"/release")

	if _, _, err := Update(context.Background(), "1.0.0"); err == nil || !strings.Contains(err.Error(), "checksums.txt") {
		t.Fatalf("want a missing-checksums error, got: %v", err)
	}
}

func TestReplaceAtSuccess(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "skillet")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	newBin := []byte("new binary")
	if err := replaceAt(exe, newBin); err != nil {
		t.Fatalf("replaceAt: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newBin) {
		t.Fatalf("exe content = %q, want %q", got, newBin)
	}
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Errorf("the .old backup should be cleaned up after a successful swap, stat err = %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(exe)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("new binary mode = %v, want the executable bit set", info.Mode().Perm())
		}
	}
}

// TestReplaceAtRestoresOnInstallFailure exercises the path where the running
// binary has already been renamed aside and the new binary fails to move into
// place. The original must come back so the user is never left without a working
// skillet.
func TestReplaceAtRestoresOnInstallFailure(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "skillet")
	orig := []byte("the original working binary")
	if err := os.WriteFile(exe, orig, 0o755); err != nil {
		t.Fatal(err)
	}

	// Fail only the install rename (temp file -> exe); let the rename-aside and the
	// restore rename go through, so the restore branch actually runs.
	stubRename(t, func(from, to string) error {
		if to == exe && strings.Contains(filepath.Base(from), ".skillet-update-") {
			return errors.New("injected install failure")
		}
		return os.Rename(from, to)
	})

	if err := replaceAt(exe, []byte("new binary that never lands")); err == nil {
		t.Fatal("replaceAt should fail when the install rename fails")
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("the original binary must be restored at %s: %v", exe, err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatalf("restored content = %q, want the original %q", got, orig)
	}
}

// TestReplaceAtReportsUnrestorableOriginal covers the worst case: the install
// rename fails and the restore fails too. replaceAt must report where the
// original was preserved so the user can recover by hand.
func TestReplaceAtReportsUnrestorableOriginal(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "skillet")
	orig := []byte("original")
	if err := os.WriteFile(exe, orig, 0o755); err != nil {
		t.Fatal(err)
	}

	// Fail every rename whose destination is exe: the install and the restore.
	stubRename(t, func(from, to string) error {
		if to == exe {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	})

	err := replaceAt(exe, []byte("new"))
	if err == nil {
		t.Fatal("expected an error when the original cannot be restored")
	}
	if !strings.Contains(err.Error(), exe+".old") {
		t.Errorf("error should point at the preserved original; got: %v", err)
	}
	got, rerr := os.ReadFile(exe + ".old")
	if rerr != nil {
		t.Fatalf("the original must be preserved at %s for manual recovery: %v", exe+".old", rerr)
	}
	if !bytes.Equal(got, orig) {
		t.Errorf("preserved original = %q, want %q", got, orig)
	}
}

// withAPI points the GitHub release lookup at a test server for the duration of
// the test.
func withAPI(t *testing.T, url string) {
	t.Helper()
	old := apiURL
	apiURL = url
	t.Cleanup(func() { apiURL = old })
}

// stubReplace swaps the executable-replacement step for a test double so a test
// never touches the real running binary, restoring the original on cleanup.
func stubReplace(t *testing.T, fn func([]byte) error) {
	t.Helper()
	old := replace
	replace = fn
	t.Cleanup(func() { replace = old })
}

// stubRename swaps the rename primitive used by replaceAt, restoring the original
// on cleanup.
func stubRename(t *testing.T, fn func(from, to string) error) {
	t.Helper()
	old := rename
	rename = fn
	t.Cleanup(func() { rename = old })
}

type releaseConfig struct {
	tag      string
	archive  []byte
	sums     []byte
	hasAsset bool
	hasSums  bool
}

// releaseServer serves a GitHub-style release whose asset URLs point back at
// itself, so Update can fetch the archive and checksums over real HTTP.
func releaseServer(t *testing.T, cfg releaseConfig) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/release", func(w http.ResponseWriter, _ *http.Request) {
		var assets []Asset
		if cfg.hasAsset {
			assets = append(assets, Asset{Name: AssetName(), URL: srv.URL + "/asset"})
		}
		if cfg.hasSums {
			assets = append(assets, Asset{Name: "checksums.txt", URL: srv.URL + "/sums"})
		}
		_ = json.NewEncoder(w).Encode(Release{Tag: cfg.tag, Assets: assets})
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(cfg.archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(cfg.sums) })
	srv = httptest.NewServer(mux)
	return srv
}

func checksumsFor(name string, data []byte) []byte {
	sum := sha256.Sum256(data)
	return []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
}

// buildPlatformArchive builds the archive shape that AssetName expects on the
// host platform, so extractBinary takes the same branch Update would in the wild.
func buildPlatformArchive(t *testing.T, bin []byte) []byte {
	t.Helper()
	if strings.HasSuffix(AssetName(), ".zip") {
		return buildZip(t, binaryName(), bin)
	}
	return buildTarGz(t, binaryName(), bin)
}

func buildTarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	writeTar(t, tw, "LICENSE", []byte("MIT")) // decoy entry
	writeTar(t, tw, name, body)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTar(t *testing.T, tw *tar.Writer, name string, body []byte) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
}

func buildZip(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writeZip(t, zw, "README.md", []byte("docs")) // decoy entry
	writeZip(t, zw, name, body)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeZip(t *testing.T, zw *zip.Writer, name string, body []byte) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
}
