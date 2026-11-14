package ui

import (
	"github.com/egoist/mygo/ui"
)

type tabDrag struct{ id int }

// tabBar shows the open repositories as tabs the user reorders by
// dragging and closes with their button.
func (a *App) tabBar(c *ui.Context) {
	th := c.Theme()
	parts := ui.TabsBase(c, &a.active, len(a.tabs))
	from, to, closing, moving := -1, -1, -1, -1
	parts.List.Row().Gap(2).Padding(4, 8, 0, 8).Background(th.Surface).Children(func() {
		for i, t := range a.tabs {
			c.Key(t.id)
			tb := parts.Tab(i).Drag(tabDrag{t.id}).Padding(6, 10).Radius(6, 6, 0, 0).Gap(6).AlignItems(ui.Center)
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
				if a.NewWindow != nil && m.Item("Move to New Window").Disabled(len(a.tabs) < 2).Chosen() {
					moving = i
				}
				if m.Item("Close Tab").Chosen() {
					closing = i
				}
			})
			tb.Label(name).Children(func() {
				ui.Text(c, name).SingleLine().MaxWidth(200)
				if ui.Button(c, "×").Label("Close "+name).Padding(0, 4).Clicked() {
					closing = i
				}
			})
		}
	})
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
