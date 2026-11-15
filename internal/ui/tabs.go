package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/locales"
)

type tabDrag struct{ id int }

// tabBar shows the open repositories as tabs the user reorders by
// dragging and closes with their button.
func (a *App) tabBar(c *ui.Context) {
	th := c.Theme()
	from, to, closing, moving := -1, -1, -1, -1
	var act func()
	// The tabs sit on the bar's line, with its buttons, as Safari's do.
	ui.Row(c).Gap(spaceM).Padding(spaceXS, insetX).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		if t := a.current(); t != nil {
			a.navBar(c, t)
		}
		parts := ui.TabsBase(c, &a.active, len(a.tabs))
		parts.List.Row().Gap(space2XS).AlignItems(ui.Center).Shrink(1).Children(func() {
			for i, t := range a.tabs {
				c.Key(t.id)
				tb := parts.Tab(i).Drag(tabDrag{t.id}).Padding(0, spaceS, 0, spaceL).MinHeight(controlHeight).Radius(6).Gap(spaceS).AlignItems(ui.Center)
				switch {
				case i == a.active:
					tb.Background(th.SurfacePressed)
				case tb.Hovered():
					tb.Background(th.SurfaceHover)
				}
				if _, over := ui.DragOver[tabDrag](tb); over {
					tb.Border(1, th.Accent)
				}
				if d, ok := ui.Drop[tabDrag](tb); ok {
					from, to = a.indexOf(d.id), i
				}
				if tb.Dragging() {
					tb.Opacity(0.5)
				}
				name := a.title(t)
				tb.ContextMenu(func(m *ui.Menu) {
					if a.NewWindow != nil && m.Item(locales.TabsMoveToNewWindow()).Disabled(len(a.tabs) < 2 || busyWrite(t) != nil).Chosen() {
						moving = i
					}
					a.editorMenu(m, t, &act)
					m.Separator()
					if m.Item(locales.TabsCloseTab()).Chosen() {
						closing = i
					}
				})
				tb.Label(name).Children(func() {
					ui.Text(c, name).SingleLine().MaxWidth(200)
					if closeButton(c, locales.TabsClose(name)).Clicked() {
						closing = i
					}
				})
			}
		})
		a.openMore(c)
		ui.Spacer(c)
		if t := a.current(); t != nil && !t.s.State.Opening && t.s.State.Err == nil {
			a.activity(c, t)
			a.worktreeButton(c, t)
			a.branchButton(c, t)
		}
		a.appButton(c)
	})
	if act != nil {
		act()
	}
	if from >= 0 {
		a.Move(from, to)
	}
	switch {
	case closing >= 0:
		a.closeTab(closing)
	case moving >= 0:
		a.moveToNewWindow(moving)
	}
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
		b.Children(func() { ui.Icon(c, navIcons().gear).FontSize(16) })
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
