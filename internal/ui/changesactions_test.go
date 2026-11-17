package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

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
