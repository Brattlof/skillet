// Package selfupdate upgrades the running skillet binary from its GitHub releases.
// It downloads the release asset for the current platform, verifies it against the
// release checksums before trusting it, and replaces the executable atomically.
//
// Trust model: checksums.txt is fetched over HTTPS from the same GitHub release and
// is the root of trust. There is no signature or transparency log, so a compromise
// of the release assets or the publishing account would not be caught here.
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
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultAPIURL = "https://api.github.com/repos/Brattlof/skillet/releases/latest"

// Overridable for tests.
var (
	apiURL     = defaultAPIURL
	httpClient = &http.Client{Timeout: 30 * time.Second}
	rename     = os.Rename
	replace    = replaceExecutable
)

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Release is the subset of the GitHub release API we use.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

func latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github releases API returned %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Release{}, err
	}
	var r Release
	if err := json.Unmarshal(b, &r); err != nil {
		return Release{}, err
	}
	if strings.TrimSpace(r.Tag) == "" {
		return Release{}, errors.New("github release response has no tag")
	}
	return r, nil
}

// Check returns the latest release tag and whether it is newer than current.
func Check(ctx context.Context, current string) (tag string, available bool, err error) {
	r, err := latest(ctx)
	if err != nil {
		return "", false, err
	}
	return r.Tag, Newer(current, r.Tag), nil
}

// Update downloads the latest release for this platform, verifies its checksum,
// and atomically replaces the running executable. updated is false with no error
// when the binary is already current.
func Update(ctx context.Context, current string) (tag string, updated bool, err error) {
	r, err := latest(ctx)
	if err != nil {
		return "", false, err
	}
	if !Newer(current, r.Tag) {
		return r.Tag, false, nil
	}

	want := AssetName()
	assetURL := findAsset(r, want)
	if assetURL == "" {
		return "", false, fmt.Errorf("release %s has no asset %q for this platform", r.Tag, want)
	}
	sumsURL := findAsset(r, "checksums.txt")
	if sumsURL == "" {
		return "", false, fmt.Errorf("release %s has no checksums.txt", r.Tag)
	}

	archive, err := download(ctx, assetURL)
	if err != nil {
		return "", false, err
	}
	sums, err := download(ctx, sumsURL)
	if err != nil {
		return "", false, err
	}
	// Verify BEFORE trusting any bytes from the archive.
	if err := verifyChecksum(archive, want, sums); err != nil {
		return "", false, err
	}

	bin, err := extractBinary(archive, want)
	if err != nil {
		return "", false, err
	}
	if err := replace(bin); err != nil {
		return "", false, err
	}
	return r.Tag, true, nil
}

// AssetName is the release asset name for the current platform.
func AssetName() string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("skillet_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext)
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "skillet.exe"
	}
	return "skillet"
}

// Newer reports whether latest is a newer semantic version than current.
func Newer(current, latest string) bool {
	return compareVersions(latest, current) > 0
}

func compareVersions(a, b string) int {
	na, pra := parseVersion(a)
	nb, prb := parseVersion(b)
	for i := 0; i < 3; i++ {
		if na[i] != nb[i] {
			if na[i] > nb[i] {
				return 1
			}
			return -1
		}
	}
	// Equal core version: a final release outranks a pre-release of it, since
	// SemVer orders 1.2.0-rc1 below 1.2.0. Among two pre-releases, compare the
	// identifiers lexically (rc1 < rc2), which covers the cases we tag.
	switch {
	case pra == "" && prb == "":
		return 0
	case pra == "":
		return 1
	case prb == "":
		return -1
	default:
		return strings.Compare(pra, prb)
	}
}

// parseVersion splits a tag into its numeric major.minor.patch triple and its
// pre-release identifier, if any. Build metadata after "+" is ignored, per SemVer.
func parseVersion(s string) (nums [3]int, pre string) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	for i, p := range strings.SplitN(s, ".", 3) {
		nums[i], _ = strconv.Atoi(strings.TrimSpace(p))
	}
	return nums, pre
}

func findAsset(r Release, name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 100<<20))
}

// verifyChecksum confirms data matches the sha256 recorded for name in a
// GoReleaser-style checksums file ("<hex>  <name>" per line).
func verifyChecksum(data []byte, name string, checksums []byte) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	var want string
	for _, line := range strings.Split(string(checksums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == name {
			want = f[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum listed for %s", name)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return nil
}

// extractBinary returns the skillet executable from a .tar.gz or .zip archive.
func extractBinary(archive []byte, assetName string) ([]byte, error) {
	target := binaryName()

	if strings.HasSuffix(assetName, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == target {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 200<<20))
			}
		}
		return nil, fmt.Errorf("%s not found in archive", target)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if path.Base(h.Name) == target {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
	return nil, fmt.Errorf("%s not found in archive", target)
}

// replaceExecutable swaps the running binary for newBin, resolving the real path
// through any symlink before handing off to replaceAt.
func replaceExecutable(newBin []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return replaceAt(exe, newBin)
}

// replaceAt swaps the binary at exe for newBin. It writes to a temp file in the
// same directory, then renames the current binary aside and the new one into
// place, restoring the original on failure so a crash never leaves a partial or
// missing binary.
func replaceAt(exe string, newBin []byte) error {
	dir := filepath.Dir(exe)

	tmp, err := os.CreateTemp(dir, ".skillet-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (reinstall manually or check permissions): %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(newBin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}

	old := exe + ".old"
	os.Remove(old) // clear any leftover from a previous interrupted update; absence is fine
	if err := rename(exe, old); err != nil {
		return fmt.Errorf("cannot replace %s (reinstall manually or check permissions): %w", exe, err)
	}
	if err := rename(tmpName, exe); err != nil {
		if rerr := rename(old, exe); rerr != nil {
			return fmt.Errorf("install failed (%w) and the original could not be restored; it is at %s: %v", err, old, rerr)
		}
		return fmt.Errorf("cannot install the new binary: %w", err)
	}
	os.Remove(old) // best effort; a running .exe on Windows may stay until exit
	return nil
}
