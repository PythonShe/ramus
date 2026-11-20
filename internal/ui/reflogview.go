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

	readFor *refs.List
	readRef string
	loading bool
	// shownRef is the ref whose reflog entries and err are.
	shownRef string
	entries  []session.ReflogEntry
	// keys name entries in the list, keys[i] entries[i].
	keys     []reflogKey
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

// reflogKey names a reflog entry in the list across reads, as its
// selector does not: each new entry renumbers those below it. An entry is
// known by its commit, and by how many older entries have that commit,
// which a new entry on top leaves as it was.
type reflogKey struct {
	oid string
	n   int
}

// reflogKeys are the keys of entries, newest first as git lists them.
func reflogKeys(entries []session.ReflogEntry) []reflogKey {
	keys := make([]reflogKey, len(entries))
	seen := make(map[string]int, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		id := string(entries[i].OID)
		keys[i] = reflogKey{id, seen[id]}
		seen[id]++
	}
	return keys
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
	label := redact.Control(strings.TrimPrefix(rs.ref, "refs/heads/"))
	givesWay(ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
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
				// By the full name: two names cleaned for showing may
				// read alike.
				if m.Item(redact.Control(strings.TrimPrefix(name, "refs/heads/"))).ID(name).Chosen() {
					rs.ref = name
				}
			}
		}
	}))
}

func (a *App) reflogList(c *ui.Context, t *tab) {
	rs := &t.reflog
	if rs.ref != rs.shownRef && (rs.entries != nil || rs.err != "") {
		// Another ref's entries are not shown, nor acted on, under this
		// one's name while its own are read.
		rs.entries, rs.keys, rs.err, rs.chosen, rs.chosenID = nil, nil, "", -1, nil
	}
	// Any ref change may have added an entry.
	if refs := t.s.State.Refs; (refs != rs.readFor || rs.ref != rs.readRef) && !rs.loading {
		rs.readFor, rs.readRef, rs.loading = refs, rs.ref, true
		ref := rs.ref
		t.s.Reflog(ref, func(es []session.ReflogEntry, err error) {
			rs.loading = false
			if ref != rs.ref || reopened(err) {
				// A reopen read the refs again: so is the reflog, the
				// entries shown staying till then.
				return
			}
			rs.shownRef, rs.entries, rs.keys, rs.err = ref, es, reflogKeys(es), ""
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
	entries, keys := rs.entries, rs.keys
	rs.list.Selected = &rs.chosen
	// A menu opened on an entry is built again for the item chosen, by
	// then perhaps at another index as entries came on top.
	rs.list.Key = func(i int) any { return keys[i] }
	keepTop(&rs.list.ListState)
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
				var search func()
				search = func() {
					t.s.LostCommits(func(l []session.LostCommit, err error) {
						if reopened(err) {
							// Searched again, unless the view was left meanwhile,
							// which ended the search.
							a.askAgain(t, func() {
								if rs.lostLoading {
									search()
								}
							})
							return
						}
						rs.lostLoading, rs.lost, rs.lostErr, rs.lostChosen = false, l, "", -1
						if err != nil {
							rs.lostErr = errText(err)
						}
					})
				}
				search()
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
	rs.lostList.Key = func(i int) any { return string(lost[i].OID) }
	keepTop(&rs.lostList.ListState)
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
