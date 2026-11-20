package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/library"
)

// Opening what is no repository (a plain folder, a file, a path that
// does not exist) does not add it to the library, where it would stay in
// the recent list for good.
func TestLibraryKeepsOnlyRepositories(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(h.closeAll)
	plain := t.TempDir()
	file := filepath.Join(plain, "notes.txt")
	os.WriteFile(file, []byte("x"), 0o600)
	for _, p := range []string{plain, filepath.Join(t.TempDir(), "typo"), file} {
		h.app.Open(p)
	}
	r := h.repoWith()
	h.app.Open(r.Dir)
	h.app.WaitWrites(time.Minute)
	if got := h.lib.Recent(10); len(got) != 1 || got[0].Path != library.Clean(r.Dir) {
		t.Fatalf("library has %+v", got)
	}
}

// A folder inside a repository, as the command line, a drop or a file's
// history opens it, is recorded as the repository, not as the folder
// that would be listed beside it; a folder inside a submodule is the
// submodule's, which stays an entry of its own.
func TestLibraryKeepsTheRepositoryOfAFolder(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(h.closeAll)
	r := h.repoWith("src/deep/a.txt")
	sub := gittest.Init(t)
	sub.Write("s/x.txt", "1")
	sub.Commit("one")
	r.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "lib")
	r.Commit("with lib")
	h.app.Open(filepath.Join(r.Dir, "src", "deep"))
	h.app.Open(filepath.Join(r.Dir, "lib", "s"))
	if !h.app.WaitWrites(time.Minute) {
		t.Fatal("the library was not written in time")
	}
	var got []string
	for _, e := range h.lib.Recent(10) {
		got = append(got, e.Path)
	}
	slices.Sort(got)
	want := []string{library.Clean(r.Dir), library.Clean(filepath.Join(r.Dir, "lib"))}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("library has %q, want %q", got, want)
	}
}

// WaitWrites, which a quit calls, returns once a repository just opened
// is in the library's file, not before.
func TestWaitWritesHasTheLibraryWritten(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.app.Open(r.Dir)
	if !h.app.WaitWrites(time.Minute) {
		t.Fatal("the library was not written in time")
	}
	back, err := library.Load(h.lib.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Recent(10); len(got) != 1 || got[0].Path != library.Clean(r.Dir) {
		t.Fatalf("library file has %+v", got)
	}
}
