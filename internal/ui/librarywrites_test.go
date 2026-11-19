package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
	h.app.WaitWrites(10 * time.Second)
	if got := h.lib.Recent(10); len(got) != 1 || got[0].Path != library.Clean(r.Dir) {
		t.Fatalf("library has %+v", got)
	}
}

// WaitWrites, which a quit calls, returns once a repository just opened
// is in the library's file, not before.
func TestWaitWritesHasTheLibraryWritten(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.app.Open(r.Dir)
	if !h.app.WaitWrites(10 * time.Second) {
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
