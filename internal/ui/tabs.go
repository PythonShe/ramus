package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/locales"
)

// tabBar is the bar over a repository: the sidebar and the way back and
// forward, the repository shown, which opens the picker of the others,
// its branch and worktrees, what it is doing, and the app's button. The
// repositories open stay open behind the one shown, as tabs would.
func (a *App) tabBar(c *ui.Context) {
	th := c.Theme()
	ui.Row(c).Gap(spaceM).Padding(spaceXS, insetX).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		t := a.current()
		if t != nil {
			a.navBar(c, t)
		}
		a.repoButton(c, t)
		ready := t != nil && !t.s.State.Opening && t.s.State.Err == nil
		if ready {
			a.branchButton(c, t)
			a.syncButton(c, t)
			a.worktreeButton(c, t)
		}
		ui.Spacer(c)
		if ready {
			a.activity(c, t)
		}
		a.appButton(c)
	})
}

func (a *App) indexOf(id int) int {
	for i, t := range a.tabs {
		if t.id == id {
			return i
		}
	}
	return -1
}

// appButton is the app's own: its settings, its updates and About, at
// the end of the bar.
func (a *App) appButton(c *ui.Context) {
	label := locales.TabsApp()
	ui.Toolbar(c, func() {
		b := ui.Button(c, "").Label(label).Tooltip(label)
		b.Children(func() { ui.Icon(c, navIcons().gear).FontSize(iconLarge) })
		b.Menu(func(m *ui.Menu) {
			if m.Item(locales.KeysSettings()).Chosen() {
				a.openSettings()
			}
			if a.ShowAbout == nil {
				return
			}
			m.Separator()
			if m.Item(locales.MenuCheckForUpdates()).Chosen() {
				a.ShowAbout(true)
			}
			if m.Item(locales.MenuAbout()).Chosen() {
				a.ShowAbout(false)
			}
		})
	}).Label(label)
}
