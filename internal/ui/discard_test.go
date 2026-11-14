package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// Discarding tracked and untracked files together does both: the delete
// of the untracked ones, queued after the discard, does not find the
// tracked files it changed and refuse as stale.
func TestDiscardTrackedAndUntracked(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("a.txt", "changed\n")
	r.Write("new.txt", "x\n")
	h.until("the changes", func() bool { return h.tt.HasText("a.txt") && h.tt.HasText("new.txt") })
	tb := h.app.current()
	logged := len(tb.s.Log)

	h.app.askDiscardPaths(tb, &tb.cv.confirm, []string{"a.txt"}, []string{"new.txt"})
	h.until("the question", h.shows("Discard 2 files?"))
	h.click("Discard")
	h.until("both writes", func() bool {
		if len(tb.s.Log) < logged+2 {
			return false
		}
		for _, e := range tb.s.Log[logged:] {
			if e.Running {
				return false
			}
		}
		return true
	})
	for _, e := range tb.s.Log[logged:] {
		if e.Err != nil {
			t.Errorf("%s: %v", e.Name, e.Err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "a.txt")); string(b) != "a\n" {
		t.Errorf("a.txt is %q", b)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("new.txt is still there: %v", err)
	}
}
