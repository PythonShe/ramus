package gitexe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
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

// On macOS without the developer tools, /usr/bin/git, a shim that offers
// to install them, is never tried, though it is on PATH.
func TestShimNeedsDevTools(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS's shim")
	}
	has := func(c []string) bool { return slices.Contains(c, shim) }
	if c := candidates("/usr/bin:/bin", func() bool { return false }); has(c) {
		t.Fatalf("without the tools: %v", c)
	}
	if c := candidates("/usr/bin:/bin", func() bool { return true }); !has(c) {
		t.Fatalf("with the tools: %v", c)
	}
}

// A git that is a wrapper script whose child hangs is given up on within
// the time limit, and its child does not run on.
func TestProbeWrapperHangs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script")
	}
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	fake := filepath.Join(dir, "git")
	os.WriteFile(fake, []byte("#!/bin/sh\nsleep 2\ntouch '"+mark+"'\necho git version 2.50.0\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Probe(ctx, fake); err == nil {
		t.Fatal("answered")
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("returned after %v", d)
	}
	time.Sleep(time.Until(start.Add(3 * time.Second)))
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("the wrapper's child ran on")
	}
}

func TestSecurityUpdate(t *testing.T) {
	for raw, want := range map[string]bool{
		"git version 2.36.0":           true,
		"git version 2.49.1":           true,
		"git version 2.50.1":           true,
		"git version 2.50.2":           false,
		"git version 2.51.2":           true,
		"git version 2.51.3":           false,
		"git version 2.56.0":           true,
		"git version 2.56.1":           false,
		"git version 2.57.0":           false,
		"git version 2.53.1.windows.1": false,
		"git version 3.0.0":            false,
	} {
		v, err := ParseVersion(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := v.SecurityUpdate(); got != want {
			t.Errorf("%s: %v, want %v", raw, got, want)
		}
	}
}
