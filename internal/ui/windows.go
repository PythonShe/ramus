package ui

import (
	"slices"

	"ramus/internal/library"
	"ramus/internal/settings"
)

// UseSettings takes settings another window changed; they take effect
// from the next frame, as when changed here.
func (a *App) UseSettings(s settings.Settings) { a.Settings = s }

// moveToNewWindow closes tab i here and opens its repository in a window
// of its own.
func (a *App) moveToNewWindow(i int) {
	if a.NewWindow == nil || i < 0 || i >= len(a.tabs) {
		return
	}
	path := a.tabs[i].s.Path
	a.Close(i)
	a.NewWindow([]string{path})
}

// Has reports whether the repository at path is open in a tab here.
func (a *App) Has(path string) bool {
	path = library.Clean(path)
	return slices.ContainsFunc(a.tabs, func(t *tab) bool { return t.s.Path == path })
}
