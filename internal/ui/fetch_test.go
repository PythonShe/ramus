package ui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Open repositories fetch in the background as the settings say, but for
// one the user keeps from it, which the library remembers.
func TestBackgroundFetchSettings(t *testing.T) {
	h := newHarness(t)
	origin := h.repoWith()
	r := h.repoWith()
	r.Git("remote", "add", "origin", origin.Dir)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	if tb.s.FetchEvery() != 0 {
		t.Fatal("fetching with the setting off")
	}
	h.app.Settings.FetchMinutes = 15
	h.until("fetching", func() bool { return tb.s.FetchEvery() == 15*time.Minute })

	h.app.palette.open = true
	h.tt.Frame()
	h.tt.Type("stop fetching")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.until("fetching off", func() bool { return tb.s.FetchEvery() == 0 && h.app.Library.FetchOff(r.Dir) })

	h.app.palette.open = true
	h.tt.Frame()
	h.tt.Type("fetch this repository")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.until("fetching again", func() bool { return tb.s.FetchEvery() == 15*time.Minute && !h.app.Library.FetchOff(r.Dir) })

	h.app.Settings.FetchMinutes = 0
	h.until("fetching off everywhere", func() bool { return tb.s.FetchEvery() == 0 })
}
