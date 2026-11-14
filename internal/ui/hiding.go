package ui

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"ramus/internal/settings"
)

// sidebarParts are the sidebar's items and sections that can be left
// out, by key, with their names.
var sidebarParts = []struct{ key, name string }{
	{"history", "History"}, {"reflog", "Reflog"}, {"refs", "Branches and Tags"}, {"stats", "Statistics"},
	{"log", "Command Log"}, {"snapshots", "Snapshots"}, {"branches", "Branches"}, {"remotes", "Remotes"},
	{"tags", "Tags"}, {"stashes", "Stashes"}, {"worktrees", "Worktrees"}, {"submodules", "Submodules"},
}

func partName(key string) string {
	for _, p := range sidebarParts {
		if p.key == key {
			return p.name
		}
	}
	return key
}

// shown reports whether the sidebar shows the part key.
func (a *App) shown(key string) bool { return !slices.Contains(a.Settings.HiddenSections, key) }

// hideMenu gives el, the sidebar's part key, a menu item leaving it out.
func (a *App) hideMenu(el ui.Element, key string) {
	el.ContextMenu(func(m *ui.Menu) {
		if m.Item("Hide " + partName(key) + " from the Sidebar").Chosen() {
			a.setShown(key, false)
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
			cmds = append(cmds, command{label: "Show " + p.name + " in the Sidebar", run: func() { a.setShown(key, true) }})
		}
	}
	if a.Settings.NoSidebar {
		cmds = append(cmds, command{id: "toggle-sidebar", label: "Show Sidebar", run: func() { a.setSidebar(true) }})
	} else {
		cmds = append(cmds, command{id: "toggle-sidebar", label: "Hide Sidebar", run: func() { a.setSidebar(false) }})
	}
	if a.Settings.NoStatusBar {
		cmds = append(cmds, command{id: "toggle-status-bar", label: "Show Status Bar", run: func() { a.Settings.NoStatusBar = false; a.keepSettings() }})
	} else {
		cmds = append(cmds, command{id: "toggle-status-bar", label: "Hide Status Bar", run: func() { a.Settings.NoStatusBar = true; a.keepSettings() }})
	}
	return cmds
}

// keepSettings saves the settings off the main thread.
func (a *App) keepSettings() {
	if a.SettingsPath == "" {
		return
	}
	path, saved, update := a.SettingsPath, a.Settings, a.Update
	go func() {
		if err := settings.Save(path, saved); err != nil {
			update(func() { a.notice = "Settings not saved: " + errText(err) })
		}
	}()
}
