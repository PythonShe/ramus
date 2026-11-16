package ui

import (
	"slices"

	"ramus/internal/library"
	"ramus/internal/settings"
)

// UseSettings takes settings another window changed; they take effect
// from the next frame, as when changed here.
func (a *App) UseSettings(s settings.Settings) {
	tools := !slices.Equal(s.Editor, a.Settings.Editor) || !slices.Equal(s.Terminal, a.Settings.Terminal)
	a.Settings = s
	if tools {
		a.findTools()
	}
	if a.settingsUI.open {
		a.loadSettingsForm()
	}
}

// moveToNewWindow closes tab i here and opens its repository in a window
// of its own.
func (a *App) moveToNewWindow(i int) {
	if a.NewWindow == nil || i < 0 || i >= len(a.tabs) {
		return
	}
	t := a.tabs[i]
	a.askToDrop(t, func() {
		if i := slices.Index(a.tabs, t); i >= 0 {
			a.Close(i)
			a.NewWindow([]string{t.s.Path})
		}
	})
}

// Has reports whether the repository at path is open in a tab here.
func (a *App) Has(path string) bool {
	path = library.Clean(path)
	return slices.ContainsFunc(a.tabs, func(t *tab) bool { return t.s.Path == path })
}
