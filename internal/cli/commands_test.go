package cli

import (
	"flag"
	"testing"

	"github.com/Brattlof/skillet/internal/install"
	"github.com/Brattlof/skillet/internal/registry"
)

func TestListStatus(t *testing.T) {
	cases := []struct {
		name   string
		hasRec bool
		inReg  bool
		rec    install.Record
		entry  registry.Entry
		want   string
	}{
		{"no record", false, true, install.Record{}, registry.Entry{}, "no record"},
		{"not in registry", true, false, install.Record{}, registry.Entry{}, "not in registry"},
		{"registry added a pin", true, true, install.Record{Ref: ""}, registry.Entry{Ref: "v2"}, "update available"},
		{"pinned and matching", true, true, install.Record{Ref: "v1"}, registry.Entry{Ref: "v1"}, "up to date"},
		{"cksum changed", true, true, install.Record{Cksum: "sha256:a"}, registry.Entry{Cksum: "sha256:b"}, "update available"},
		{"unpinned tracking", true, true, install.Record{}, registry.Entry{}, "tracking"},
		{"cksum pinned and matching", true, true, install.Record{Cksum: "sha256:a"}, registry.Entry{Cksum: "sha256:a"}, "up to date"},
	}
	for _, tc := range cases {
		if got := listStatus(tc.hasRec, tc.inReg, tc.rec, tc.entry); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// parseArgs must accept flags before, after, or interspersed with positionals.
func TestParseArgsFlagPositions(t *testing.T) {
	cases := [][]string{
		{"--dir", "/tmp/x", "hello"}, // flag first
		{"hello", "--dir", "/tmp/x"}, // flag after positional (the bug)
		{"hello", "--dir=/tmp/x"},    // = form after positional
	}
	for _, args := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		dir := fs.String("dir", "", "")
		pos, err := parseArgs(fs, args)
		if err != nil {
			t.Fatalf("args %v: %v", args, err)
		}
		if len(pos) != 1 || pos[0] != "hello" {
			t.Fatalf("args %v: positionals = %v, want [hello]", args, pos)
		}
		if *dir != "/tmp/x" {
			t.Fatalf("args %v: dir = %q, want /tmp/x", args, *dir)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},         // shorter than limit, unchanged
		{"hello", 5, "hello"},          // exactly the limit, unchanged
		{"hello world", 8, "hello..."}, // truncated: 5 runes + ellipsis
		{"hello", 3, "hel"},            // n <= 3, no room for ellipsis
		{"hello", 1, "h"},              // small n must not panic
		{"hello", 0, ""},               // zero n must not panic
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
