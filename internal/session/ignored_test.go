package session

import (
	"slices"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/repo"
)

// A new set skips what it holds, and gives back what it no longer holds,
// to watch again.
func TestIgnoredDirsReplace(t *testing.T) {
	var d ignoredDirs
	if d.skip("build") {
		t.Fatal("skipped before any read")
	}
	if got := d.replace([]string{"build", "node_modules"}); len(got) != 0 {
		t.Fatalf("unskipped %q on the first read", got)
	}
	if !d.skip("build") || !d.skip("node_modules") || d.skip("src") || d.skip("build/x") {
		t.Fatal("skips the wrong folders")
	}
	got := d.replace([]string{"node_modules", "target"})
	if !slices.Equal(got, []string{"build"}) {
		t.Fatalf("unskipped %q", got)
	}
	if d.skip("build") || !d.skip("target") {
		t.Fatal("the new set is not the one skipped")
	}
	for _, n := range []repo.Notice{{What: repo.Rescan}, {What: repo.WorkTreeChanged, Paths: []string{"a", "sub/.gitignore"}}} {
		if !changesIgnores(n) {
			t.Fatalf("%+v changes no ignores", n)
		}
	}
	if changesIgnores(repo.Notice{What: repo.WorkTreeChanged, Paths: []string{"a.gitignore", "b"}}) {
		t.Fatal("a file that is not a .gitignore changes ignores")
	}
}

// Where watches are per directory, the session reads the ignored folders
// before the watcher walks the work tree, and again when a .gitignore
// changes.
func TestIgnoredDirsFollowGitignore(t *testing.T) {
	was := perDirectory
	perDirectory = true
	t.Cleanup(func() { perDirectory = was })
	r := gittest.Init(t)
	r.Write(".gitignore", "build/\n")
	r.Write("a", "1")
	r.Commit("one")
	r.Write("build/out.o", "x")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil })
	d := s.ignored.Load()
	if d == nil || !d.skip("build") {
		t.Fatal("build/ is not skipped")
	}
	r.Write(".gitignore", "other/\n")
	// The read runs beside the refresh, which may be the last update.
	for deadline := time.Now().Add(20 * time.Second); d.skip("build"); m.pump(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("build/ is still skipped")
		}
	}
}
