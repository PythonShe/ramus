package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// Two folders compare as the files that differ, each shown as a patch;
// closing the comparison tells whoever waits on it.
func TestCompareFolders(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	old, new := filepath.Join(root, "old"), filepath.Join(root, "new")
	for p, s := range map[string]string{
		"old/s/x": "1\n", "new/s/x": "2\n", "old/gone.txt": "g\n", "new/added.txt": "a\n", "old/same": "s\n", "new/same": "s\n",
	} {
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	closed := 0
	h.app.Compare(old, new, func() { closed++ })
	h.until("the listing", func() bool { return hasExact(h, "s/x") && hasExact(h, "gone.txt") && hasExact(h, "added.txt") })
	if hasExact(h, "same") || !h.tt.HasText("3 files differ") {
		t.Fatalf("listing: %q", h.tt.Texts())
	}
	cs := &h.app.compareUI
	t.Cleanup(func() {
		if t.Failed() && cs.patch.Parsed != nil {
			for i := range cs.patch.Parsed.Files {
				t.Logf("patch file %d: %q", i, cs.patch.Parsed.Path(i))
			}
		}
		if t.Failed() {
			t.Logf("patch error %v, key %+v", cs.patch.Err, cs.key)
		}
	})
	shown := func(want string) func() bool {
		return func() bool {
			p := cs.patch.Parsed
			return p != nil && len(p.Files) == 1 && filepath.Base(p.Path(0)) == want && cs.pv.d != nil
		}
	}
	h.until("the first patch", func() bool { return cs.patch.Parsed != nil && cs.pv.d != nil })
	h.click("s/x")
	h.until("the patch of s/x", shown("x"))
	h.click("Close")
	h.until("closed", func() bool { return closed == 1 && !cs.open })

	// Two files, without a listing.
	h.app.Compare(filepath.Join(old, "s", "x"), filepath.Join(new, "s", "x"), nil)
	h.until("the file patch", shown("x"))
	if cs.dir {
		t.Fatal("two files compared as folders")
	}
	h.app.Compare(old, filepath.Join(new, "same"), nil)
	h.until("the mismatch", h.shows("are not both files or both folders"))
}

// Git's copies of a file compared show as the file's name and the names
// of its sides, not as their paths.
func TestCompareNamed(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	old, new := filepath.Join(dir, "git-blob-x", "a.txt"), filepath.Join(dir, "a.txt")
	for p, s := range map[string]string{old: "1\n", new: "2\n"} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.app.CompareNamed(old, new, "src/a.txt", []string{"HEAD", "Working tree"}, nil)
	h.until("the patch", func() bool { return h.app.compareUI.pv.d != nil })
	if !hasExact(h, "src/a.txt") || !h.tt.HasText("HEAD → Working tree") || h.tt.HasText("git-blob-x") {
		t.Fatalf("header: %q", h.tt.Texts())
	}
}

// The patch of two files compared is read by a process that closing the
// comparison stops, as it does one read for a file no longer shown.
func TestComparePatchStops(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the version control tool")
	}
	real := gittest.GitPath(t)
	bin := t.TempDir()
	// Its patches of two files outside a repository never come.
	script := "#!/bin/sh\ncase \"$*\" in *--no-index*) exec sleep 60;; esac\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitexe.Find(context.Background(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	r := run.New(g, gittest.Env(), 8)
	h := newHarnessWith(t, r)
	dir := t.TempDir()
	old, new := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	for p, s := range map[string]string{old: "1\n", new: "2\n"} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.app.Compare(old, new, nil)
	h.until("the patch read", func() bool { return r.Running() == 1 })
	h.app.compareUI.open = false
	h.tt.Frame()
	deadline := time.Now().Add(5 * time.Second)
	for r.Running() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the read of the patch runs on after the comparison closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A read of a file's patch stopped as another file is chosen hands back
// nothing, even once that file is chosen again: its error would show in
// place of the patch read anew.
func TestComparePatchStoppedIsDropped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the version control tool")
	}
	real := gittest.GitPath(t)
	bin := t.TempDir()
	// It lists the files that differ, but their patches come only once
	// stopped.
	script := "#!/bin/sh\ncase \"$*\" in *--name-status*) ;; *--no-index*) exec sleep 60;; esac\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitexe.Find(context.Background(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	r := run.New(g, gittest.Env(), 8)
	h := newHarnessWith(t, r)
	root := t.TempDir()
	old, new := filepath.Join(root, "old"), filepath.Join(root, "new")
	for p, s := range map[string]string{"old/a.txt": "1\n", "new/a.txt": "2\n", "old/b.txt": "1\n", "new/b.txt": "2\n"} {
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.app.Compare(old, new, nil)
	h.until("the patch of a.txt read", func() bool { return hasExact(h, "b.txt") && r.Running() == 1 })
	cs := &h.app.compareUI
	// No update runs until both stopped reads handed theirs back.
	h.click("b.txt")
	h.click("a.txt")
	for deadline := time.Now().Add(10 * time.Second); len(h.updates) < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of the stopped reads ended", len(h.updates))
		}
	}
	for len(h.updates) > 0 {
		(<-h.updates)()
	}
	h.tt.Frame()
	if cs.patch.Err != nil || !cs.patchLoading {
		t.Fatalf("a.txt read anew shows %v, loading %v", cs.patch.Err, cs.patchLoading)
	}
	cs.open = false
	h.tt.Frame()
}
