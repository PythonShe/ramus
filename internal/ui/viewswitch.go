package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/internal/session"
	"ramus/locales"
)

// viewSwitchW is the width of each half of the view switch: fixed, so a
// count of changes growing a digit moves nothing after it in the bar.
// It holds "Changes" and a count of four digits; more is cut.
const viewSwitchW = 108

// viewSwitch is the bar's switch between the two pages used all day,
// Changes and History, so that the sidebar can stay hidden. Neither half
// is down while another page shows: one click goes back. They are
// toggles, not a radio group, as a radio group has always one chosen.
func (a *App) viewSwitch(c *ui.Context, t *tab) {
	th := c.Theme()
	st := t.s.State
	km := a.keys()
	views := []struct {
		sel, name, label, action, id string
		count                        int
	}{
		{sideChanges, locales.RepoSectionChanges(), changesLabel(st), locales.KeysShowChanges(), "show-changes", changedFiles(st)},
		{"history", locales.RepoSectionHistory(), locales.RepoSectionHistory(), locales.KeysShowHistory(), "show-history", 0},
	}
	ui.ToggleGroup(c, func() {
		for _, v := range views {
			on := t.sel == v.sel
			tip := v.action
			if ks := km.byID[v.id]; len(ks) > 0 {
				tip = locales.TabsViewTooltip(v.action, ks[0].label())
			}
			b := ui.Toggle(c, &on, "").Label(v.label).Tooltip(tip).Width(viewSwitchW).Shrink(0)
			b.Children(func() {
				ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Shrink(1).Children(func() {
					ui.Text(c, v.name).SingleLine().Shrink(0)
					if v.count > 0 {
						ui.Text(c, locales.TabsChangesCount(v.count)).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Shrink(1)
					}
				})
			})
			if b.Changed() {
				// A click on the half already down leaves it down.
				t.sel = v.sel
			}
		}
	}).Label(locales.TabsViews()).Shrink(0)
}

// changedFiles is how many files the working tree has changed: the
// entries of the status, and what the folders it lists whole hold past
// the entry each is.
func changedFiles(st session.State) int {
	if st.Status == nil {
		return 0
	}
	n := st.Status.Len()
	if st.Folders != nil {
		n += st.Folders.Extra
	}
	return n
}
