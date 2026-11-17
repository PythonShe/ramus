package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/write"
)

// A merge reverts against the parent the user names, which git needs;
// the revert done, a toast offers to undo it.
func TestRevertMergeAndUndo(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("checkout", "-q", "-b", "side")
	r.Write("s.txt", "s\n")
	r.Commit("on side")
	r.Git("checkout", "-q", "main")
	r.Write("m.txt", "m\n")
	r.Commit("on main")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("merge side"))

	h.menu("merge side", "Revert Commit", "Keeping Parent 1")
	h.idle(tb, "the revert")
	if l := tb.s.Log; l[len(l)-1].Err != nil || h.app.notice != "" {
		t.Fatalf("%s: %v; notice %q", l[len(l)-1].Name, l[len(l)-1].Err, h.app.notice)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "s.txt")); !os.IsNotExist(err) {
		t.Fatal("what the merge brought in is still there")
	}
	h.until("the toast", h.shows("Reverted 1 commit"))
	h.click("Undo")
	h.until("undone", func() bool { return strings.TrimSpace(r.Git("rev-parse", "HEAD")) == merge })
}

// The reflog knows a merge as History does: it is cherry-picked or
// reverted against the parent the user names.
func TestReflogMergeParents(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("checkout", "-q", "-b", "side")
	r.Write("s.txt", "s\n")
	r.Commit("on side")
	r.Git("checkout", "-q", "main")
	r.Write("m.txt", "m\n")
	r.Commit("on main")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("Reflog")
	// The strategy git names in the entry depends on its version.
	h.rightClickRow(ui.RoleRow, "merge side:")
	if err := h.tt.ChooseMenuItem("Revert Commit", "Keeping Parent 1"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.idle(tb, "the revert")
	if l := tb.s.Log; l[len(l)-1].Err != nil || h.app.notice != "" {
		t.Fatalf("%s: %v; notice %q", l[len(l)-1].Name, l[len(l)-1].Err, h.app.notice)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "s.txt")); !os.IsNotExist(err) {
		t.Fatal("what the merge brought in is still there")
	}
}

// A toast's Undo takes back its own write only: a write of the same name
// made since, by a route with no toast, is left alone.
func TestUndoToastOnlyItsOwn(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	one := r.Commit("one")
	r.Write("b.txt", "b\n")
	two := r.Commit("two")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	op, err := write.Revert(tb.s.Where(), []string{one}, tb.s.Seen())
	h.app.submitUndoable(tb, op, err, "Reverted one", false)
	h.until("the toast", h.shows("Reverted one"))
	h.idle(tb, "the revert done")
	op, err = write.Revert(tb.s.Where(), []string{two}, tb.s.Seen())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tb.s.Submit(op); err != nil {
		t.Fatal(err)
	}
	h.idle(tb, "the second revert done")
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	h.click("Undo")
	h.until("the refusal", h.shows("Nothing undone: other changes came since"))
	h.idle(tb, "nothing undone")
	if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
		t.Fatal("the toast's Undo took back another write of its name")
	}
}

// A file a commit changed has a menu: its path copied, its history.
func TestCommitFileMenu(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("add alpha"))
	h.click("add alpha")
	h.until("the detail", h.shows("1 file changed"))
	h.menu("a.txt", "Copy Path")
	if got := h.tt.Clipboard(); got != "a.txt" {
		t.Fatalf("copied %q", got)
	}
	h.menu("a.txt", "Show File History")
	h.until("the file's history", func() bool { return tb.sel == "file" && tb.file.path == "a.txt" })
}

// Several commits picked copy their hashes, oldest first.
func TestCopyPickedHashes(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, f := range []string{"a", "b"} {
		r.Write(f+".txt", f+"\n")
		r.Commit("add " + f)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("add b"))
	hs := tb.s.History
	for k := 0; k < hs.Snap.Len(); k++ {
		if k < 2 {
			tb.hist.picked.Add(string(hs.OID(hs.Snap.Commit(k))))
		}
	}
	h.menu("add b", "Copy 2 Hashes")
	want := r.Git("rev-parse", "HEAD~1", "HEAD")
	if got := h.tt.Clipboard(); got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
	h.menu("add b", "Copy 2 Subjects")
	h.until("the subjects", func() bool { return h.tt.Clipboard() == "add a\nadd b\n" })
}

// Escape in the history list lets go of the commits picked, the one
// chosen kept chosen and picked.
func TestEscapeUnpicks(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, f := range []string{"a", "b"} {
		r.Write(f+".txt", f+"\n")
		r.Commit("add " + f)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("add b"))
	hv := &tb.hist
	h.until("the top chosen", func() bool { return hv.chosenID != nil })
	chosen := string(hv.chosenID)
	hs := tb.s.History
	for k := range 3 {
		hv.picked.Add(string(hs.OID(hs.Snap.Commit(k))))
	}
	hv.focusList = true
	h.tt.Frame()
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if hv.picked.Len() != 1 || !hv.picked.Has(chosen) || string(hv.chosenID) != chosen {
		t.Fatalf("%d picked after Escape", hv.picked.Len())
	}
}

// Several commits picked, a merge among them, are not cherry-picked nor
// reverted together: git would stop at the merge, half done.
func TestPickedWithMergeNotPicked(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("checkout", "-q", "-b", "side")
	r.Write("s.txt", "s\n")
	r.Commit("on side")
	r.Git("checkout", "-q", "main")
	r.Write("m.txt", "m\n")
	r.Commit("on main")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	r.Write("after.txt", "a\n")
	r.Commit("after")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("merge side"))
	hs := tb.s.History
	for k := 0; k < 2; k++ {
		tb.hist.picked.Add(string(hs.OID(hs.Snap.Commit(k))))
	}
	if err := h.tt.RightClick("after"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	for _, item := range []string{"Cherry-Pick 2 Commits onto main", "Revert 2 Commits"} {
		if err := h.tt.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("%s with a merge picked: %v; menu %q", item, err, h.tt.Menu())
		}
	}
}

// Narrow, a commit's labels go above what they label, which keeps the
// whole width: beside them, the hash and the date would break apart.
func TestCommitDetailNarrow(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("add alpha"))
	h.click("add alpha")
	h.until("the detail", h.shows("1 file changed"))
	parent := strings.TrimSpace(r.Git("rev-parse", "--short=10", "HEAD~1"))
	beside := func() bool {
		h.settle()
		l, _ := h.tt.Find("Parent")
		v, _ := h.tt.Find(parent)
		return v.Y < l.Y+l.H/2
	}
	if !beside() {
		t.Fatal("the parent under its label in a wide pane")
	}
	tb.hist.detail.file = 0
	h.until("the patch", func() bool { return tb.hist.detail.patchOpen() })
	tb.hist.detail.infoW = leastInfoW
	if beside() {
		t.Fatalf("the parent beside its label at %v wide", tb.hist.detail.metaW)
	}
}
