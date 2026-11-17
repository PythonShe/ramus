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
	h.app.Settings.NoFetch = true
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	if tb.s.FetchEvery() != 0 {
		t.Fatal("fetching with the setting off")
	}
	h.app.Settings.NoFetch = false
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

	h.app.Settings.NoFetch = true
	h.until("fetching off everywhere", func() bool { return tb.s.FetchEvery() == 0 })
}

// Settings shows fetching on by default; turned off, it keeps the
// minutes chosen for when it is turned on again.
func TestFetchSettingDefault(t *testing.T) {
	h := newHarness(t)
	h.app.openSettings()
	s := &h.app.settingsUI
	if s.fetchMinutes != 15 {
		t.Fatalf("by default every %v", s.fetchMinutes)
	}
	h.tt.Frame()
	h.click("Every 15 minutes")
	h.click("Every 30 minutes")
	if h.app.Settings.FetchEvery() != 30 {
		t.Fatalf("every %d", h.app.Settings.FetchEvery())
	}
	h.app.openSettings()
	s.fetchMinutes = 0
	h.tt.Frame()
	if h.app.Settings.FetchEvery() != 0 || h.app.Settings.FetchMinutes != 30 {
		t.Fatalf("turned off: %+v", h.app.Settings)
	}
	h.app.openSettings()
	if s.fetchMinutes != 0 {
		t.Fatalf("shown again every %v", s.fetchMinutes)
	}
}
