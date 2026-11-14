package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// The dashboard lists every worktree with its branch and how many files
// changed there; two picked compare, and one opens in its tab.
func TestWorktreeDashboard(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	other := filepath.Join(base, "agent")
	r.Git("worktree", "add", "-q", "-b", "agent-work", other)
	for _, f := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(other, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the worktrees", h.shows("All Worktrees"))
	h.click("All Worktrees")
	h.until("the dashboard", h.shows("2 worktrees"))
	tb := h.app.current()
	h.until("the counts", func() bool {
		d := tb.dash.sums
		return len(d) == 2 && d[other].Changes == 2 && d[tb.s.State.Worktrees[0].Path].Changes == 0
	})
	if !h.tt.HasText("agent-work") {
		t.Fatalf("no branch; texts %q", h.tt.Texts())
	}

	h.click("Compare " + filepath.Base(r.Dir))
	h.click("Compare agent")
	h.click("Compare Branches")
	h.until("the comparison", func() bool { return tb.sel == "compare" })

	tb.sel = "worktrees"
	h.tt.Frame()
	tb.dash.chosen = 1
	h.click("Compare Folders")
	h.until("the folder comparison", func() bool { return h.app.compareUI.open })
	h.click("Close")
	h.until("the comparison closed", func() bool { return !h.app.compareUI.open })

	h.click("Open agent")
	h.until("its tab", func() bool { return len(h.app.tabs) == 2 })
}
