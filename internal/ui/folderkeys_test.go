package ui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A folder of the changes tree opens and closes by the keys, as a tree's
// does, and Enter opens it rather than staging what is in it.
func TestChangesFolderKeys(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("dir/deep.txt", "x\n")
	r.Commit("dir")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("dir/deep.txt", "changed\n")
	h.until("the change", h.shows("deep.txt"))
	h.click("Tree")
	h.until("the folder", func() bool { return hasExact(h, "dir/") })
	h.click("dir/")
	if !h.tt.HasText("Folder dir, closed") {
		t.Fatalf("the folder's label does not say it is closed: %q", h.tt.Texts())
	}

	h.tt.Key(0, ui.KeyRight)
	h.until("opened by Right", h.shows("deep.txt"))
	if !h.tt.HasText("Folder dir, open") {
		t.Fatalf("the folder's label does not say it is open: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyLeft)
	h.until("closed by Left", func() bool { return !h.tt.HasText("deep.txt") })
	h.tt.Key(0, ui.KeyEnter)
	h.until("opened by Enter", h.shows("deep.txt"))
	if s := r.Git("status", "--porcelain"); !strings.HasPrefix(s, " M") {
		t.Fatalf("Enter staged the folder: %q", s)
	}
}

// A folder of the branch list opens and closes by Right and Left.
func TestBranchFolderKeys(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "team/topic")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	h.click("Branches and Tags")
	h.until("the folder", func() bool { return hasExact(h, "team/") && hasExact(h, "topic") })
	h.click("team/")
	h.tt.Key(0, ui.KeyLeft)
	h.until("closed by Left", func() bool { return !hasExact(h, "topic") })
	if !h.tt.HasText("Folder team, closed") {
		t.Fatalf("the folder's label does not say it is closed: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyRight)
	h.until("opened by Right", func() bool { return hasExact(h, "topic") })
}
