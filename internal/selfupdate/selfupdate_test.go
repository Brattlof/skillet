package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	writeTar(t, tw, "LICENSE", []byte("MIT")) // decoy entry
	writeTar(t, tw, binaryName(), want)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := extractBinary(buf.Bytes(), "skillet_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz")
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("extracted %q, want %q", got, want)
	}
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

func TestCheckViaHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","assets":[]}`))
	}))
	defer srv.Close()

	old := apiURL
	apiURL = srv.URL
	defer func() { apiURL = old }()

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
