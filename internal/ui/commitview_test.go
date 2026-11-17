package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	h.until("the repository", h.shows("No changes"))
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

// A file a commit changed has a menu: its path copied, its history.
func TestCommitFileMenu(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
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
	h.until("the repository", h.shows("No changes"))
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

// Narrow, a commit's labels go above what they label, which keeps the
// whole width: beside them, the hash and the date would break apart.
func TestCommitDetailNarrow(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
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
