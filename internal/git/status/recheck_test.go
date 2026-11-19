package status

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// A status merged from a recheck of a few paths is the status git gives
// for the whole work tree; what a recheck cannot place is refused.
func TestReplace(t *testing.T) {
	r := gittest.Init(t)
	for _, f := range []string{"a", "b", "c", "d/e", "d/f", "z"} {
		r.Write(f, f+"\n")
	}
	r.Write("moved", "long enough to be found as a rename\n")
	r.Commit("files")
	r.Write("b", "changed\n")
	r.Write("d/e", "changed\n")
	r.Write("loose", "untracked\n")
	r.Write("newdir/x", "untracked dir\n")
	r.Git("mv", "moved", "renamed")
	run := testrun.Runner(t)
	ctx := context.Background()
	full := func() *Status {
		s, err := Read(ctx, run, r.GitDir(), r.Dir, 20, Options{})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	recheck := func(s *Status, paths ...string) (*Status, bool) {
		t.Helper()
		if !CanRecheck(paths) {
			t.Fatalf("%v cannot be rechecked", paths)
		}
		part, err := ReadPaths(ctx, run, r.GitDir(), r.Dir, 20, paths, Options{})
		if err != nil {
			t.Fatal(err)
		}
		return s.Replace(paths, part)
	}
	step := func(name string, change func(), paths ...string) {
		t.Helper()
		before := full()
		change()
		got, ok := recheck(before, paths...)
		if !ok {
			t.Fatalf("%s: refused", name)
		}
		if want := format(full()); format(got) != want {
			t.Fatalf("%s:\n got %q\nwant %q", name, format(got), want)
		}
		if got.Branch != before.Branch {
			t.Fatalf("%s: branch %+v", name, got.Branch)
		}
	}
	step("a clean file changed", func() { r.Write("a", "now changed\n") }, "a")
	step("a changed file back as it was", func() { r.Write("b", "b\n") }, "b")
	step("a changed file changed again", func() { r.Write("d/e", "again\n") }, "d/e")
	step("a file deleted", func() { os.Remove(filepath.Join(r.Dir, "z")) }, "z")
	step("an untracked file edited", func() { r.Write("loose", "more\n") }, "loose")
	step("an untracked file deleted", func() { os.Remove(filepath.Join(r.Dir, "loose")) }, "loose")
	step("several at once, one unchanged", func() {
		r.Write("c", "c2\n")
		r.Write("a", "a\n")
	}, "a", "c", "d/f")
	step("a staged change edited", func() {
		r.Write("c", "c3\n")
		r.Git("add", "c")
		r.Write("c", "c4\n")
	}, "c")

	// A write to an ignored file, or a clean one touched, leaves the
	// status itself.
	if err := os.WriteFile(filepath.Join(r.GitDir(), "info", "exclude"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := full()
	if !full().Equal(before) {
		t.Fatal("two reads of the same status differ")
	}
	r.Write("build/out.o", "x")
	r.Write("a", "a\n")
	if got, ok := recheck(before, "build/out.o", "a"); !ok || got != before {
		t.Fatalf("a recheck of nothing listed: %v, a new status %v", ok, got != before)
	}
	if !full().Equal(before) {
		t.Fatal("the status changed")
	}
	r.Write("a", "a6\n")
	if full().Equal(before) {
		t.Fatal("a status with a file newly changed is equal")
	}

	refused := func(name string, change func(), paths ...string) {
		t.Helper()
		before := full()
		change()
		if _, ok := recheck(before, paths...); ok {
			t.Fatalf("%s: a recheck was trusted", name)
		}
	}
	refused("a new file", func() { r.Write("new", "n\n") }, "new")
	refused("a file in an untracked folder", func() { r.Write("newdir/y", "y\n") }, "newdir/y")
	refused("a renamed file", func() { r.Write("renamed", "long enough to be found as a rename, edited\n") }, "renamed")
	refused("a folder", func() { r.Write("d/f", "f2\n") }, "d")
	if CanRecheck([]string{"d/"}) || CanRecheck(nil) || CanRecheck(make([]string, MaxRecheckPaths+1)) {
		t.Fatal("CanRecheck took a folder, nothing, or too many")
	}
}
