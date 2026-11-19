package ui

import (
	"testing"
	"time"

	"ramus/internal/library"
)

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
