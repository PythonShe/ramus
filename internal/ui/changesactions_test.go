package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// With a filter typed, the section's button stages the files shown and
// says so; the Stage All command stages all.
func TestStageAllWithAFilter(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.go", "b.txt", "c.txt")
	r.Commit("files")
	for _, f := range []string{"a.go", "b.txt", "c.txt"} {
		r.Write(f, "changed\n")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the changes", h.shows("c.txt"))
	cv := &h.app.current().cv
	cv.filter = "txt"
	h.until("the filter", h.shows("Stage 2 Shown"))
	h.click("Stage 2 Shown")
	h.until("the two shown staged", func() bool { return cv.model != nil && len(cv.model.all(secStaged)) == 2 })
	if got := len(cv.model.all(secUnstaged)); got != 1 {
		t.Fatalf("%d left unstaged", got)
	}
	h.app.runCommand("stage-all")
	h.until("all staged", func() bool { return cv.model != nil && len(cv.model.all(secStaged)) == 3 })
}

// Enter stages every file picked, as Delete discards them all, not only
// the one the keyboard is on.
func TestStagingActsOnEveryFilePicked(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt", "b.txt", "c.txt")
	r.Commit("files")
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		r.Write(f, "changed\n")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the changes", h.shows("c.txt"))
	cv := &h.app.current().cv
	h.click("a.txt")
	h.tt.Key(ui.Shift, ui.KeyDown)
	h.tt.Key(ui.Shift, ui.KeyDown)
	h.until("three picked", func() bool { return cv.picked.Len() == 3 })
	h.tt.Key(0, ui.KeyEnter)
	h.until("all three staged", func() bool {
		m := cv.model
		return m != nil && len(m.files[secStaged]) == 3 && len(m.files[secUnstaged]) == 0
	})
}
