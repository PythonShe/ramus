package ui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/library"
	"ramus/locales"
)

type managerState struct {
	open      bool
	query     string
	list      ui.ListState
	chosen    int
	groupName string
	// repos is what the list shows, read when the query changes, since
	// the library is behind a lock and its search sorts.
	repos []library.Repo
	shown string
	dirty bool
}

func (a *App) openManager() {
	m := &a.manager
	m.open, m.dirty = true, true
}

// managerView lists every known repository, searchable, with favourites
// and groups.
func (a *App) managerView(c *ui.Context) {
	m := &a.manager
	if !m.open || a.Library == nil {
		return
	}
	th := c.Theme()
	if m.dirty || m.shown != m.query {
		m.repos, m.shown, m.dirty = a.Library.Search(m.query), m.query, false
		m.list = ui.ListState{}
		m.chosen = -1
	}
	repos := m.repos
	var act func()
	a.modal(c, &m.open, func() {
		dialogBox(c).Width(dialogWide).Height(560).Children(func() {
			ui.Text(c, locales.ManagerTitle()).Bold()
			ui.SearchField(c, &m.query).Label(locales.ManagerSearchLabel()).Placeholder(locales.ManagerSearchPlaceholder()).AutoFocus()
			m.list.Selected = &m.chosen
			m.list.Key = func(i int) any { return repos[i].Path }
			m.list.Label = func(i int) string { return repos[i].Name }
			l := ui.List(c, &m.list, len(repos), func(i int) {
				r := repos[i]
				ui.Row(c).Padding(rowTallPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
					star := glyphs().star
					if r.Favourite {
						star = glyphs().starFill
					}
					if iconButton(c, star, favLabel(r)).Padding(0, spaceXS).Clicked() {
						path, on := r.Path, !r.Favourite
						act = func() { a.libraryWrite(func(l *library.Library) error { return l.SetFavourite(path, on) }) }
					}
					if ui.Column(c).Grow(1).Children(func() {
						ui.Text(c, r.Name).SingleLine()
						ui.Text(c, r.Path).SingleLine().FontSize(textSmall).TextColor(th.TextMuted)
					}).ContextMenu(func(menu *ui.Menu) {
						if menu.Item(locales.ManagerOpen()).Chosen() {
							p := r.Path
							act = func() { a.Open(p); m.open = false }
						}
						if a.Settings.FetchEvery() > 0 && menu.Item(locales.ManagerFetchInBackground()).Checked(!r.NoFetch).Chosen() {
							p, off := r.Path, !r.NoFetch
							act = func() { a.setFetchOff(p, off) }
						}
						menu.Separator()
						if menu.Item(locales.ManagerForget()).Chosen() {
							p := r.Path
							act = func() { a.libraryWrite(func(l *library.Library) error { return l.Forget(p) }) }
						}
					}).RowClicked() {
						p := r.Path
						act = func() { a.Open(p); m.open = false }
					}
				})
			}).Label(locales.ManagerKnown()).Grow(1)
			if l.Submitted() && m.chosen >= 0 && m.chosen < len(repos) {
				p := repos[m.chosen].Path
				act = func() { a.Open(p); m.open = false }
			}
			a.groupsPanel(c, &act)
		})
	})
	if act != nil {
		act()
	}
}

func favLabel(r library.Repo) string {
	if r.Favourite {
		return locales.ManagerUnmarkFavourite(r.Name)
	}
	return locales.ManagerMarkFavourite(r.Name)
}

func (a *App) groupsPanel(c *ui.Context, act *func()) {
	m := &a.manager
	th := c.Theme()
	groups := a.Library.Groups()
	ui.Column(c).Gap(spaceM).Children(func() {
		ui.Text(c, locales.ManagerGroups()).Bold()
		for _, g := range groups {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.ManagerGroupCount(g.Name, len(g.Paths))).Grow(1)
				if ui.Button(c, locales.ManagerOpenGroup(g.Name)).Clicked() {
					paths := g.Paths
					*act = func() {
						for _, p := range paths {
							a.Open(p)
						}
						m.open = false
					}
				}
				if ui.Button(c, locales.ManagerDeleteGroup(g.Name)).Clicked() {
					name := g.Name
					*act = func() { a.libraryWrite(func(l *library.Library) error { return l.SetGroup(name, nil) }) }
				}
			})
		}
		if len(a.tabs) == 0 {
			if len(groups) == 0 {
				ui.Text(c, locales.ManagerNoGroups()).TextColor(th.TextMuted)
			}
			return
		}
		ui.Row(c).Gap(spaceM).Children(func() {
			ui.TextInput(c, &m.groupName).Label(locales.ManagerGroupName()).Placeholder(locales.ManagerGroupName()).Grow(1)
			if ui.Button(c, locales.ManagerSaveGroup()).Disabled(strings.TrimSpace(m.groupName) == "").Clicked() {
				name := m.groupName
				var paths []string
				for _, t := range a.tabs {
					paths = append(paths, t.s.Path)
				}
				*act = func() {
					m.groupName = ""
					a.libraryWrite(func(l *library.Library) error { return l.SetGroup(name, paths) })
				}
			}
		})
	})
}

// libraryWrite changes the library off the main thread (it saves a
// file), then shows the result.
func (a *App) libraryWrite(fn func(*library.Library) error) {
	lib, update := a.Library, a.Update
	a.goWrite(func() {
		err := fn(lib)
		update(func() {
			a.manager.dirty = true
			if err != nil {
				a.notice = errText(err)
			}
		})
	})
}
