package gitexe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"git version 2.56.1":                 "2.56.1",
		"git version 2.39.5 (Apple Git-154)": "2.39.5",
		"git version 2.47.1.windows.1\n":     "2.47.1",
		"git version 2.45.0.rc1":             "2.45.0",
		"git version 3.0":                    "3.0.0",
	}
	for in, want := range cases {
		v, err := ParseVersion(in)
		if err != nil || v.String() != want {
			t.Errorf("%q: %v %v, want %s", in, v, err, want)
		}
	}
	for _, bad := range []string{"", "hg version 1", "git version x.y"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestFeatures(t *testing.T) {
	v := Version{Major: 2, Minor: 55, Patch: 0}
	if !v.Has(History) || !v.Has(HistoryFixup) || v.Has(HistoryDrop) || v.Has(BranchDeleteMerged) || !v.Has(FSMonitorLinux) {
		t.Fatal("2.55 features wrong")
	}
	old := Version{Major: 2, Minor: 39, Patch: 5}
	if old.Has(History) || !old.Has(BatchCommand) || !old.Has(MergeTreeWriteTree) {
		t.Fatal("2.39 features wrong")
	}
	if (Version{Major: 3}).AtLeast(2, 99, 0) != true {
		t.Fatal("3.0 not after 2.99")
	}
	for f := Feature(0); f < numFeatures; f++ {
		if f.String() == "" {
			t.Errorf("feature %d unnamed", f)
		}
	}
}

// The features the table says the installed git has must exist in it.
func TestFeaturesAgainstGit(t *testing.T) {
	g, err := Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	probes := map[Feature][]string{
		History:            {"history", "-h"},
		HookList:           {"hook", "list", "-h"},
		BranchDeleteMerged: {"branch", "-h"},
	}
	for f, args := range probes {
		if !g.Version.Has(f) {
			continue
		}
		out, _ := exec.Command(g.Path, args...).CombinedOutput()
		if f == BranchDeleteMerged {
			if !contains(out, "--delete-merged") {
				t.Errorf("git %s should have %s", g.Version, f)
			}
			continue
		}
		if contains(out, "is not a git command") || contains(out, "unknown subcommand") {
			t.Errorf("git %s should have %s: %s", g.Version, f, out)
		}
	}
}

func contains(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }

func TestFindSkipsRelativeAndMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	dir := t.TempDir()
	// A fake git in a relative PATH entry must never be chosen.
	fake := filepath.Join(dir, "git")
	os.WriteFile(fake, []byte("#!/bin/sh\necho git version 9.9.9\n"), 0o755)
	t.Chdir(dir)
	g, err := Find(context.Background(), ".:"+filepath.Join(dir, "missing"))
	if err == nil && g.Path == fake {
		t.Fatal("chose git from a relative PATH entry")
	}
	g, err = Find(context.Background(), dir)
	if err != nil || g.Path != fake || g.Version.String() != "9.9.9" {
		t.Fatalf("absolute PATH entry: %v %v", g, err)
	}
}
