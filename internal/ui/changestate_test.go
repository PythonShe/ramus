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
	// A new folder shows as one, not as a patch of no differences.
	h.click("newdir/")
	h.until("the folder", h.shows("newdir/ is a new folder git does not track. Stage it to see the files in it."))
}

// Each change shows the lines it adds and deletes, staged and not, a
// file git does not track counted whole, and each section their sum.
func TestChangeLines(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "1\n2\n3\n")
	r.Write("b.txt", "x\n")
	r.Commit("files")
	r.Write("a.txt", "1\n2\n3\n4\n")
	r.Git("add", "a.txt")
	r.Write("a.txt", "0\n2\n3\n4\n")
	if err := os.Remove(filepath.Join(r.Dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	r.Write("new.txt", "a\nb\nc")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	rows := []string{
		"staged modified a.txt, 1 line added, 0 deleted",
		"modified a.txt, 1 line added, 1 deleted",
		"deleted b.txt, 0 lines added, 1 deleted",
		"untracked new.txt, 3 lines added, 0 deleted",
	}
	h.until("the changes counted", func() bool {
		_, ok := h.tt.Access(rows[len(rows)-1])
		return ok
	})
	for _, name := range rows {
		if _, ok := h.tt.Access(name); !ok {
			t.Errorf("no row reads %q", name)
		}
	}
	// Staged +1; the rest +4 −2.
	for _, s := range []string{"+1", "+4", "−2"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %s in %q", s, h.tt.Texts())
		}
	}
}
