package ui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/session"
	"ramus/locales"
)

// reflogState is a tab's reflog view: where HEAD or a branch has been,
// and the commits nothing reaches any more.
type reflogState struct {
	// lostShown shows lost commits instead of the reflog.
	lostShown int
	ref       string

	readFor  *refs.List
	readRef  string
	loading  bool
	entries  []session.ReflogEntry
	err      string
	list     tableState
	chosen   int
	chosenID []byte

	lost         []session.LostCommit
	lostLoading  bool
	lostErr      string
	lostSearched bool
	lostList     tableState
	lostChosen   int

	detail  commitView
	detailW float32
}

func newReflogState() reflogState {
	return reflogState{ref: "HEAD", chosen: -1, lostChosen: -1, detailW: 560, detail: commitView{}}
}

func (a *App) reflogView(c *ui.Context, t *tab) {
	rs := &t.reflog
	chosen := rs.lostShown == 0 && rs.chosen >= 0 && rs.chosen < len(rs.entries) ||
		rs.lostShown == 1 && rs.lostChosen >= 0 && rs.lostChosen < len(rs.lost)
	ls := &rs.list.ListState
	if rs.lostShown == 1 {
		ls = &rs.lostList.ListState
	}
	listBesideCommit(c, &rs.detailW, 560, &rs.detail, chosen, ls, func() {
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).Padding(barPadY, insetX).MinHeight(barHeight).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Segmented(c, &rs.lostShown, locales.RepoSectionReflog(), locales.ReflogLostCommits())
				if rs.lostShown == 0 {
					a.reflogRefMenu(c, t)
				}
			})
			if rs.lostShown == 0 {
				a.reflogList(c, t)
			} else {
				a.lostList(c, t)
			}
		})
	}, func() {
		var id []byte
		switch {
		case rs.lostShown == 0 && rs.chosen >= 0 && rs.chosen < len(rs.entries):
			id = rs.entries[rs.chosen].OID
		case rs.lostShown == 1 && rs.lostChosen >= 0 && rs.lostChosen < len(rs.lost):
			id = rs.lost[rs.lostChosen].OID
		}
		a.commitDetail(c, t, id, &rs.detail)
	}).Fill()
}

// reflogRefMenu chooses whose reflog is shown: HEAD or a local branch.
func (a *App) reflogRefMenu(c *ui.Context, t *tab) {
	rs := &t.reflog
	label := strings.TrimPrefix(rs.ref, "refs/heads/")
	ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
		if m.Item("HEAD").Chosen() {
			rs.ref = "HEAD"
		}
		if list := t.s.State.Refs; list != nil {
			n := 0
			for i := range list.Len() {
				name := list.Name(i)
				if !strings.HasPrefix(name, "refs/heads/") {
					continue
				}
				if n++; n > sideLimit {
					break
				}
				if m.Item(strings.TrimPrefix(name, "refs/heads/")).Chosen() {
					rs.ref = name
				}
			}
		}
	})
}

func (a *App) reflogList(c *ui.Context, t *tab) {
	rs := &t.reflog
	// Any ref change may have added an entry.
	if refs := t.s.State.Refs; (refs != rs.readFor || rs.ref != rs.readRef) && !rs.loading {
		rs.readFor, rs.readRef, rs.loading = refs, rs.ref, true
		ref := rs.ref
		t.s.Reflog(ref, func(es []session.ReflogEntry, err error) {
			rs.loading = false
			if ref != rs.ref {
				return
			}
			rs.entries, rs.err = es, ""
			if err != nil {
				rs.err = errText(err)
			}
			// Keep the chosen entry chosen as new ones come on top.
			rs.chosen = -1
			for i, e := range es {
				if rs.chosenID != nil && string(e.OID) == string(rs.chosenID) {
					rs.chosen = i
					break
				}
			}
		})
	}
	switch {
	case rs.err != "":
		errorMessage(c, rs.err)
		return
	case rs.entries == nil && rs.loading:
		loading(c, "")
		return
	case len(rs.entries) == 0:
		emptyState(c, locales.ReflogNoEntries())
		return
	}
	entries := rs.entries
	rs.list.Selected = &rs.chosen
	rs.list.Label = func(i int) string {
		return locales.ReflogEntry(entries[i].Selector, redact.Control(entries[i].Message))
	}
	rs.list.CellMenu = func(m *ui.Menu, i, _ int) { a.commitItems(m, t, entries[i].OID, entries[i].Merges) }
	cols := []ui.TableColumn{
		{Title: locales.ColumnEntry(), ID: "entry", Width: 110, MinWidth: 60},
		{Title: locales.ColumnMessage(), ID: "message", MinWidth: 80},
		dateColumn(), commitColumn(),
	}
	now := a.Now()
	a.savedTable(c, "reflog", &rs.list, cols, len(entries), func(i, col int) {
		e := entries[i]
		k := rs.list.ink(c, i)
		switch col {
		case 0:
			mutedCell(c, k, e.Selector).Font("monospace")
		case 1:
			ui.Text(c, redact.Control(e.Message)).SingleLine()
		case 2:
			mutedCell(c, k, ago(now, e.Time))
		case 3:
			hashCell(c, k, e.OID)
		}
	}).Label(locales.ReflogEntries()).Grow(1)
	if rs.chosen >= 0 && rs.chosen < len(entries) {
		rs.chosenID = entries[rs.chosen].OID
	}
}

func (a *App) lostList(c *ui.Context, t *tab) {
	rs := &t.reflog
	if !rs.lostSearched {
		ui.Column(c).Fill().Center().Gap(spaceL).Children(func() {
			if ui.Button(c, locales.ReflogFindLost()).Clicked() {
				rs.lostSearched, rs.lostLoading = true, true
				t.s.LostCommits(func(l []session.LostCommit, err error) {
					rs.lostLoading, rs.lost, rs.lostErr, rs.lostChosen = false, l, "", -1
					if err != nil {
						rs.lostErr = errText(err)
					}
				})
			}
		})
		return
	}
	switch {
	case rs.lostLoading:
		loading(c, "")
		return
	case rs.lostErr != "":
		errorMessage(c, rs.lostErr)
		return
	case len(rs.lost) == 0:
		emptyState(c, locales.ReflogNoLost())
		return
	}
	lost := rs.lost
	rs.lostList.Selected = &rs.lostChosen
	rs.lostList.Label = func(i int) string { return locales.ReflogLost(redact.Control(lost[i].Subject), lost[i].Author) }
	rs.lostList.CellMenu = func(m *ui.Menu, i, _ int) { a.commitItems(m, t, lost[i].OID, lost[i].Merges) }
	now := a.Now()
	a.savedTable(c, "lostCommits", &rs.lostList, commitColumns(), len(lost), func(i, col int) {
		l := lost[i]
		k := rs.lostList.ink(c, i)
		switch col {
		case 0:
			ui.Text(c, redact.Control(l.Subject)).SingleLine()
		case 1:
			mutedCell(c, k, redact.Control(l.Author))
		case 2:
			mutedCell(c, k, ago(now, l.Time))
		case 3:
			hashCell(c, k, l.OID)
		}
	}).Label(locales.ReflogLostList()).Grow(1)
}

// ago says how long before now t was, roughly.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return locales.ReflogJustNow()
	case d < time.Hour:
		return locales.ReflogMinutesAgo(int(d / time.Minute))
	case d < 24*time.Hour:
		return locales.ReflogHoursAgo(int(d / time.Hour))
	case d < 60*24*time.Hour:
		return locales.ReflogDaysAgo(int(d / (24 * time.Hour)))
	}
	return t.Format("2006-01-02")
}
