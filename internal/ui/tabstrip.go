package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/locales"
)

// tabStrip is what the strip of tabs under the bar keeps from frame to
// frame.
type tabStrip struct {
	// room is the strip's width as last laid out, which its tabs share.
	room float32
	// middle is the tab the middle button pressed, by id, 0 none: it
	// closes once the button lets go over it, as a click would.
	middle int
	// shown is the tab last scrolled into view, by id: the strip follows
	// the tab chosen, by the keys or the palette, but leaves the user to
	// scroll it otherwise.
	shown int
}

// tabDrag is a tab dragged along the strip, by id, to put elsewhere.
type tabDrag struct{ id int }

// A tab's widths: the tabs share the strip's room between these, and
// past their least the strip scrolls. tabCloseW is its close button's,
// which a tab not hovered keeps empty, so its name does not move.
const (
	tabLeastW = 104
	tabMostW  = 220
	tabCloseW = iconSmall + 2*spaceXS
)

// tabWidth is the width of each of n tabs sharing room, less the gaps
// and the strip's insets.
func tabWidth(room float32, n int) float32 {
	if room <= 0 || n == 0 {
		return tabMostW
	}
	w := (room - 2*insetX - float32(n-1)*space2XS) / float32(n)
	return max(tabLeastW, min(tabMostW, w))
}

// tabsStrip shows the repositories open in the window as tabs, under the
// bar, while there is more than one: each named as the palette names it,
// with a dot when its working tree has changes and a spinner while a
// write the user asked for runs there. A click shows it, a drag puts it
// elsewhere, its button or the middle button closes it as Close Tab
// would, asking first when that stops a write.
func (a *App) tabsStrip(c *ui.Context) {
	n := len(a.tabs)
	if n < 2 {
		return
	}
	th := c.Theme()
	s := &a.strip
	w := tabWidth(s.room, n)
	from, to, closing, moving := -1, -1, -1, -1
	var act func()
	sc := ui.ScrollHorizontal(c).FillWidth().Background(th.Surface)
	sc.Children(func() {
		parts := ui.TabsBase(c, &a.active, n)
		parts.List.Label(locales.TabsList()).Gap(space2XS).Padding(0, insetX, spaceXS, insetX).AlignItems(ui.Center).Children(func() {
			for i, t := range a.tabs {
				c.Key(t.id)
				tb := parts.Tab(i).Drag(tabDrag{t.id}).Width(w).MinHeight(controlHeight).Padding(0, spaceXS).Radius(th.Radius).Gap(spaceXS).AlignItems(ui.Center)
				on, hover := i == a.active, tb.Hovered()
				switch {
				case on:
					tb.Background(th.SurfacePressed)
				case hover:
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
				if b, ok := tb.PressedButton(); ok && b == 2 {
					s.middle = t.id
				} else if s.middle == t.id {
					s.middle = 0
					if hover {
						closing = i
					}
				}
				if on && s.shown != t.id {
					s.shown = t.id
					tb.ScrollIntoView()
				}
				name := a.paletteTitle(t)
				state, write := "", ""
				if e := busyWrite(t); e != nil {
					state, write = "running", e.Name
				} else if st := t.s.State.Status; st != nil && st.Len() > 0 {
					state = "changed"
				}
				tb.Label(locales.TabsTab(state, name, write)).Tooltip(t.s.Path)
				tb.ContextMenu(func(m *ui.Menu) {
					if a.NewWindow != nil && m.Item(locales.TabsMoveToNewWindow()).Disabled(busyWrite(t) != nil).Chosen() {
						moving = i
					}
					a.editorMenu(m, t, &act)
					m.Separator()
					if m.Item(locales.TabsCloseTab()).Chosen() {
						closing = i
					}
				})
				tb.Children(func() {
					// The close button first, as Safari has it, in a slot
					// kept empty when it does not show, and another as
					// wide after the name, which stays centred.
					ui.Row(c).Width(tabCloseW).Shrink(0).Children(func() {
						if (on || hover) && closeButton(c, locales.TabsClose(name)).Shrink(0).Clicked() {
							closing = i
						}
					})
					ui.Spacer(c)
					txt := ui.Text(c, name).SingleLine().TruncateMiddle().Shrink(1)
					if !on {
						txt.TextColor(th.TextMuted)
					}
					switch state {
					case "running":
						ui.Spinner(c).FontSize(textSmall).Shrink(0)
					case "changed":
						ui.Icon(c, glyphs().dot).FontSize(iconMedium).TextColor(th.TextMuted).Shrink(0)
					}
					ui.Spacer(c)
					ui.Box(c).Width(tabCloseW).Shrink(0)
				})
			}
		})
	})
	if room := sc.LayoutBounds().W; room != s.room {
		// The tabs take their share of the new room in the next frame.
		s.room = room
		c.AnimationFrame()
	}
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
