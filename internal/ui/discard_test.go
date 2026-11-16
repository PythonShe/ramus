package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Discarding a file added with intent to add takes the intent back and
// leaves its content: putting it back as the index has it would empty it.
func TestDiscardIntentToAdd(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("ita.txt", "my work\n")
	r.Git("add", "-N", "ita.txt")
	h.until("the change", h.shows("ita.txt"))
	tb := h.app.current()
	st := tb.s.State.Status
	found := false
	for i := range st.Len() {
		if string(st.Path(i)) == "ita.txt" {
			found = intentToAdd(st, i)
		}
	}
	if !found {
		t.Fatal("ita.txt is not seen as added with intent to add")
	}
	logged := len(tb.s.Log)
	h.app.askDiscardPaths(tb, &tb.cv.confirm, nil, nil, []string{"ita.txt"})
	h.until("the question", h.shows("Discard 1 file?"))
	h.click("Discard")
	h.until("the write", func() bool { return len(tb.s.Log) > logged && !tb.s.Log[len(tb.s.Log)-1].Running })
	if e := tb.s.Log[len(tb.s.Log)-1]; e.Err != nil {
		t.Fatalf("%s: %v", e.Name, e.Err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "ita.txt")); string(b) != "my work\n" {
		t.Fatalf("ita.txt is %q", b)
	}
	if ls := strings.TrimSpace(r.Git("ls-files", "ita.txt")); ls != "" {
		t.Fatalf("ita.txt is still in the index: %q", ls)
	}
}

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

	h.app.askDiscardPaths(tb, &tb.cv.confirm, []string{"a.txt"}, []string{"new.txt"}, nil)
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
