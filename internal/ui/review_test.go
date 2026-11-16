package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/editor"
)

// The review lists every file with changes not staged, each patch in
// full; a change or a file is kept (staged) or discarded from it, and
// the files marked seen leave it until they change again.
func TestReviewAll(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n")
	r.Write("b.txt", "b\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("a.txt", "one\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\ntwelve\n")
	r.Write("b.txt", "bee\n")
	r.Write("new.txt", "x\n")
	tb := h.app.current()
	h.until("the changes", func() bool { st := tb.s.State.Status; return st != nil && st.Len() == 3 })
	h.click("Review")
	h.until("the review", h.shows("3 files to review"))
	rv := &tb.rev
	h.until("every patch", func() bool {
		for _, p := range []string{"a.txt", "b.txt", "new.txt"} {
			if it := rv.items[p]; it == nil || it.pv.d == nil {
				return false
			}
		}
		return true
	})
	index := func() string { return r.Git("show", ":a.txt") }
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(r.Dir, name)); return string(b) }

	// Keep the first change of a.txt alone, from the buttons on it.
	pv := &rv.items["a.txt"].pv
	if len(pv.runs) != 2 {
		t.Fatalf("%d changes in a.txt", len(pv.runs))
	}
	// The first viewer is a.txt's, whose first lines are its first change.
	at, ok := h.tt.Find("New text")
	if !ok {
		t.Fatalf("no viewer; texts %q", h.tt.Texts())
	}
	h.tt.Move(at.X+at.W/2, at.Y+8)
	h.until("the change's buttons", h.shows("Keep"))
	h.click("Keep")
	h.until("the first change kept", func() bool { return strings.HasPrefix(index(), "one\n") })
	if strings.Contains(index(), "twelve") {
		t.Fatal("the second change was kept too")
	}

	// Keep the new file whole: it leaves the review.
	h.click("Keep new.txt")
	h.until("new.txt kept", func() bool { return r.Git("diff", "--cached", "--name-only") == "a.txt\nnew.txt\n" })
	h.until("two left", h.shows("2 files to review"))

	// Discard b.txt, after the question.
	h.click("Discard b.txt…")
	h.until("the question", h.shows("Discard 1 file?"))
	h.click("Discard")
	h.until("b.txt discarded", func() bool { return read("b.txt") == "b\n" })
	h.until("one left", h.shows("1 file to review"))

	// Mark what is left seen; it shows again once it changes.
	h.until("the files read", func() bool { return rv.stampedFor == tb.s.State.Status && rv.stamps != nil })
	h.click("Mark All Seen")
	h.click("Changed since seen")
	h.until("nothing new", h.shows("Nothing changed since you marked it seen"))
	r.Write("a.txt", "one\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\ntwelve\nthirteen\n")
	h.until("the change since", h.shows("1 file changed since seen"))
}

// The review has a layout of its own, side by side until changed, and
// choosing one there leaves that of the patch in Changes.
func TestReviewLayoutOfItsOwn(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "1\n2\n3\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("a.txt", "1\ntwo\n3\n")
	tb := h.app.current()
	h.until("the change", h.shows("a.txt"))

	h.click("a.txt")
	h.until("its patch", func() bool { return tb.cv.diff.pv.d != nil })
	if tb.cv.diff.pv.d.Layout != editor.DiffUnified {
		t.Fatal("the patch in Changes is not unified")
	}
	h.click("Review")
	h.until("the review's patch", func() bool { it := tb.rev.items["a.txt"]; return it != nil && it.pv.d != nil })
	if tb.rev.items["a.txt"].pv.d.Layout != editor.DiffSplit {
		t.Fatal("the review is not side by side")
	}

	h.click("Unified")
	if tb.rev.items["a.txt"].pv.d.Layout != editor.DiffUnified {
		t.Fatal("the review did not take the layout chosen")
	}
	if !h.app.Settings.ReviewUnified || h.app.Settings.DiffSplit {
		t.Fatalf("settings %+v", h.app.Settings)
	}
	h.click("List")
	h.click("Split")
	h.until("the patch in Changes", func() bool { return tb.cv.diff.pv.d != nil && tb.cv.diff.pv.d.Layout == editor.DiffSplit })
	if !h.app.Settings.ReviewUnified || !h.app.Settings.DiffSplit {
		t.Fatalf("settings %+v", h.app.Settings)
	}
}
