package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
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
		"untracked folder newdir/, open",
	} {
		// Alone, or with its lines counted after it, which may have come
		// by now.
		if !slices.ContainsFunc(h.tt.AccessAll(), func(a ui.AccessInfo) bool {
			return a.Name == name || strings.HasPrefix(a.Name, name+", ")
		}) {
			t.Errorf("no row reads %q", name)
		}
	}
	if h.tt.HasText("?") || !h.tt.HasText("U") || !h.tt.HasText("R") || !h.tt.HasText("D") {
		t.Errorf("letters %q", h.tt.Texts())
	}
	// A new folder lists its files under it, not a patch of no
	// differences.
	h.click("newdir/")
	h.until("the folder", func() bool { return h.tt.HasText("1 file") && h.tt.HasText("x.txt") })
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
	// All of them: what is staged and what is not are not counted at once.
	h.until("the changes counted", func() bool {
		for _, name := range rows {
			if _, ok := h.tt.Access(name); !ok {
				return false
			}
		}
		return true
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

// Staging the file chosen chooses the one now in its place, so Enter
// stages one after the other; the last of a section is followed to
// where it went. Delete asks to discard the file chosen.
func TestStagingKeepsThePlace(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt", "b.txt", "c.txt")
	r.Commit("files")
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		r.Write(f, "changed\n")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the changes", h.shows("c.txt"))
	tb := h.app.current()
	cv := &tb.cv
	chosen := func() string {
		m := cv.model
		if m == nil || cv.chosen < 0 || cv.chosen >= len(m.rows) || m.rows[cv.chosen].kind != rowFile {
			return ""
		}
		r := m.rows[cv.chosen]
		return r.sec.title() + " " + string(m.st.Path(int(r.idx)))
	}
	h.click("b.txt")
	h.until("b chosen", func() bool { return chosen() == secUnstaged.title()+" b.txt" })
	h.tt.Key(0, ui.KeyEnter)
	h.until("c in b's place", func() bool { return chosen() == secUnstaged.title()+" c.txt" })
	h.tt.Key(0, ui.KeyEnter)
	h.until("a, the last left", func() bool { return chosen() == secUnstaged.title()+" a.txt" })
	h.tt.Key(0, ui.KeyEnter)
	h.until("a followed into Staged", func() bool { return chosen() == secStaged.title()+" a.txt" })

	r.Write("a.txt", "again\n")
	h.until("a changed again", func() bool {
		return cv.model != nil && len(cv.model.files[secUnstaged]) == 1
	})
	for i, r := range cv.model.rows {
		if r.kind == rowFile && r.sec == secUnstaged {
			cv.list.Choose(i)
		}
	}
	h.until("a chosen", func() bool { return chosen() == secUnstaged.title()+" a.txt" })
	h.tt.Key(0, ui.KeyDelete)
	h.until("asked to discard", func() bool { return cv.confirm.open })
}
