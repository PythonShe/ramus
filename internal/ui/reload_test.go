package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/repo"
)

// Refresh reads the shown patch again rather than handing back the one
// kept, and keeps the commit message being written.
func TestReload(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "one\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("a.txt", "two\n")
	// Old enough for its patch to be kept.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(r.Dir, "a.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	h.until("the change", h.shows("a.txt"))
	h.click("a.txt")
	tb := h.app.current()
	h.until("its patch", patchHas(&tb.cv.diff.pv, "two", "one"))
	tb.commit.subject = "half written"
	first := tb.cv.diff.res.Parsed
	tb.s.Refresh(repo.Rescan)
	h.settle()
	if tb.cv.diff.res.Parsed != first {
		t.Fatal("a refresh read the kept patch again")
	}

	h.tt.Key(ui.Cmd, ui.KeyR)
	h.until("the patch read again", func() bool {
		return tb.cv.diff.res.Parsed != first && patchHas(&tb.cv.diff.pv, "two", "one")()
	})
	if tb.commit.subject != "half written" {
		t.Fatalf("the message became %q", tb.commit.subject)
	}
}
