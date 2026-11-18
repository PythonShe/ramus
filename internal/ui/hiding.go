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
	{"refs", locales.RepoSectionRefs},
	{"branches", locales.RepoSectionBranches}, {"remotes", locales.RepoSectionRemotes}, {"tags", locales.RepoSectionTags},
	{"stashes", locales.RepoSectionStashes}, {"worktrees", locales.RepoSectionWorktrees}, {"submodules", locales.RepoSectionSubmodules},
	{"snapshots", locales.RepoSectionSnapshots}, {"reflog", locales.RepoSectionReflog}, {"log", locales.RepoSectionLog},
	{"stats", locales.RepoSectionStats}, {sideLocks, locales.RepoSectionLocks},
	{sidePulls, locales.RepoSectionPulls},
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
	return cmds
}

// keepSettings saves the settings off the main thread, and tells the
// other windows, which would write back what they have otherwise.
func (a *App) keepSettings() { a.keepSettingsOr(locales.HidingSettingsNotSaved) }

// keepSettingsOr is keepSettings, notice saying why they were not saved.
func (a *App) keepSettingsOr(notice func(err string) string) {
	if a.SettingsChanged != nil {
		a.SettingsChanged(a.Settings)
	}
	a.writeSettings(a.Settings, notice)
}

// writeSettings has s written to the settings file; notice says why it
// was not, from the error.
func (a *App) writeSettings(s settings.Settings, notice func(err string) string) {
	if a.SettingsPath == "" {
		return
	}
	k := a.SettingsKeeper
	if k == nil || k.Path() != a.SettingsPath {
		if a.keeper == nil || a.keeper.Path() != a.SettingsPath {
			a.keeper = settings.NewKeeper(a.SettingsPath)
		}
		k = a.keeper
	}
	update := a.Update
	k.Keep(s, func(err error) { update(func() { a.notice = notice(errText(err)) }) })
}
