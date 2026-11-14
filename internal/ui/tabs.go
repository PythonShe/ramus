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
	ui.Row(c).Gap(spaceM).Padding(0, 0, 0, insetX).AlignItems(ui.End).Background(th.Surface).Children(func() {
		if t := a.current(); t != nil {
			ui.Row(c).AlignSelf(ui.Center).Children(func() { a.navBar(c, t) })
		}
		parts := ui.TabsBase(c, &a.active, len(a.tabs))
		parts.List.Row().Gap(space2XS).Padding(spaceXS, insetX, 0, 0).Grow(1).Children(func() {
			for i, t := range a.tabs {
				c.Key(t.id)
				tb := parts.Tab(i).Drag(tabDrag{t.id}).Padding(spaceS, spaceL).Radius(6, 6, 0, 0).Gap(spaceS).AlignItems(ui.Center)
				if i == a.active {
					tb.Background(th.Background)
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
					if a.NewWindow != nil && m.Item(locales.TabsMoveToNewWindow()).Disabled(len(a.tabs) < 2).Chosen() {
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
					if ui.Button(c, "×").Label(locales.TabsClose(name)).Padding(0, spaceXS).Clicked() {
						closing = i
					}
				})
			}
		})
	})
	if act != nil {
		act()
	}
	if from >= 0 {
		a.Move(from, to)
	}
	switch {
	case closing >= 0:
		a.Close(closing)
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
