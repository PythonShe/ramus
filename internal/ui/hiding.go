package ui

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"ramus/internal/settings"
	"ramus/locales"
)

// sidebarParts are the sidebar's items and sections that can be left
// out, by key, with their names, said in the language shown when asked.
var sidebarParts = []struct {
	key  string
	name func() string
}{
	{"review", locales.RepoSectionReview}, {"history", locales.RepoSectionHistory}, {"reflog", locales.RepoSectionReflog},
	{"refs", locales.RepoSectionRefs}, {"stats", locales.RepoSectionStats}, {"log", locales.RepoSectionLog},
	{"snapshots", locales.RepoSectionSnapshots}, {"branches", locales.RepoSectionBranches}, {"remotes", locales.RepoSectionRemotes},
	{"tags", locales.RepoSectionTags}, {"stashes", locales.RepoSectionStashes}, {"worktrees", locales.RepoSectionWorktrees},
	{"submodules", locales.RepoSectionSubmodules},
}

func partName(key string) string {
	for _, p := range sidebarParts {
		if p.key == key {
			return p.name()
		}
	}
	return key
}

// shown reports whether the sidebar shows the part key.
func (a *App) shown(key string) bool { return !slices.Contains(a.Settings.HiddenSections, key) }

// hideMenu gives el, the sidebar's part key, a menu item leaving it out.
func (a *App) hideMenu(el ui.Element, key string) { a.partMenu(el, key, nil) }

// partMenu is hideMenu with the part's own items first.
func (a *App) partMenu(el ui.Element, key string, items func(*ui.Menu, *func())) {
	el.ContextMenu(func(m *ui.Menu) {
		var act func()
		if items != nil {
			items(m, &act)
		}
		if m.Item(locales.HidingHideFromSidebar(partName(key))).Chosen() {
			act = func() { a.setShown(key, false) }
		}
		if act != nil {
			act()
		}
	})
}

// setShown shows or leaves out the sidebar's part key, and keeps that.
func (a *App) setShown(key string, show bool) {
	hidden := slices.DeleteFunc(slices.Clone(a.Settings.HiddenSections), func(k string) bool { return k == key })
	if !show {
		hidden = append(hidden, key)
	}
	a.Settings.HiddenSections = hidden
	a.keepSettings()
}

// setSidebar shows or leaves out the whole sidebar, and keeps that.
func (a *App) setSidebar(show bool) {
	a.Settings.NoSidebar = !show
	a.keepSettings()
}

// hidingCommands are the palette's commands bringing back what was left
// out, and leaving out the sidebar.
func (a *App) hidingCommands() []command {
	var cmds []command
	for _, p := range sidebarParts {
		if !a.shown(p.key) {
			key := p.key
			cmds = append(cmds, command{label: locales.HidingShowInSidebar(p.name()), run: func() { a.setShown(key, true) }})
		}
	}
	if a.Settings.NoSidebar {
		cmds = append(cmds, command{id: "toggle-sidebar", label: locales.HidingShowSidebar(), run: func() { a.setSidebar(true) }})
	} else {
		cmds = append(cmds, command{id: "toggle-sidebar", label: locales.HidingHideSidebar(), run: func() { a.setSidebar(false) }})
	}
	if a.Settings.NoStatusBar {
		cmds = append(cmds, command{id: "toggle-status-bar", label: locales.HidingShowStatusBar(), run: func() { a.Settings.NoStatusBar = false; a.keepSettings() }})
	} else {
		cmds = append(cmds, command{id: "toggle-status-bar", label: locales.HidingHideStatusBar(), run: func() { a.Settings.NoStatusBar = true; a.keepSettings() }})
	}
	return cmds
}

// keepSettings saves the settings off the main thread.
func (a *App) keepSettings() {
	if a.SettingsChanged != nil {
		a.SettingsChanged(a.Settings)
	}
	if a.SettingsPath == "" {
		return
	}
	path, saved, update := a.SettingsPath, a.Settings, a.Update
	go func() {
		if err := settings.Save(path, saved); err != nil {
			update(func() { a.notice = locales.HidingSettingsNotSaved(errText(err)) })
		}
	}()
}
