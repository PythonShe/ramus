package ui

import (
	"testing"

	"ramus/internal/git/watch"
)

// A repository past the system's watch limit says so and still refreshes
// on focus, even with focus refresh turned off.
func TestWatchLimit(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("a.txt"))
	tb := h.app.current()
	h.app.Settings.NoFocusRefresh, h.app.Settings.NoStatusBar = true, true
	tb.s.State.WatchErr = watch.ErrLimit
	h.until("the warning", h.shows("Too many folders to watch for changes; refreshing when the window is focused"))
	r.Write("late.txt", "x")
	h.app.WindowFocused()
	h.until("the change", h.shows("late.txt"))
}
