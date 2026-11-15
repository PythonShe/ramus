package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// Each change reads as what it is: a folder git does not track as one,
// in words of its own, what git does not track as U, a move made with
// git mv as a rename, and a plain move as a deletion and a new file.
func TestChangeStates(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Write("m.txt", "m\n")
	r.Write("p.txt", "p\n")
	r.Commit("files")
	r.Write("a.txt", "b\n")
	r.Git("mv", "m.txt", "moved.txt")
	if err := os.Rename(filepath.Join(r.Dir, "p.txt"), filepath.Join(r.Dir, "q.txt")); err != nil {
		t.Fatal(err)
	}
	r.Write("newdir/x.txt", "x\n")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the changes", h.shows("newdir/"))
	for _, name := range []string{
		"modified a.txt",
		"staged renamed moved.txt",
		"deleted p.txt",
		"untracked q.txt",
		"untracked folder newdir/",
	} {
		if _, ok := h.tt.Access(name); !ok {
			t.Errorf("no row reads %q", name)
		}
	}
	if h.tt.HasText("?") || !h.tt.HasText("U") || !h.tt.HasText("R") || !h.tt.HasText("D") {
		t.Errorf("letters %q", h.tt.Texts())
	}
}
