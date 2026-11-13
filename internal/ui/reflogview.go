package ui

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/session"
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
	list     ui.ListState
	chosen   int
	chosenID []byte

	lost         []session.LostCommit
	lostLoading  bool
	lostErr      string
	lostSearched bool
	lostList     ui.ListState
	lostChosen   int

	detail  commitView
	detailW float32
}

func newReflogState() reflogState {
	return reflogState{ref: "HEAD", chosen: -1, lostChosen: -1, detailW: 560, detail: commitView{split: 360}}
}

func (a *App) reflogView(c *ui.Context, t *tab) {
	rs := &t.reflog
	ui.Split(c, &rs.detailW, func() {
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Segmented(c, &rs.lostShown, "Reflog", "Lost Commits")
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
	}).SplitReset(560).Fill()
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
	th := c.Theme()
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
		ui.Text(c, rs.err).TextColor(th.Danger).Padding(12).Selectable()
		return
	case rs.entries == nil && rs.loading:
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	case len(rs.entries) == 0:
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "No reflog entries").TextColor(th.TextMuted)
		})
		return
	}
	entries := rs.entries
	rs.list.Selected = &rs.chosen
	rs.list.Label = func(i int) string { return entries[i].Selector + ", " + redact.Control(entries[i].Message) }
	now := a.Now()
	ui.List(c, &rs.list, len(entries), func(i int) {
		e := entries[i]
		row := ui.Row(c).Padding(3, 12).Gap(8).AlignItems(ui.Center).FillWidth()
		row.Children(func() {
			ui.Text(c, e.Selector).Font("monospace").FontSize(12).Width(96).TextColor(th.TextMuted).SingleLine()
			ui.Text(c, redact.Control(e.Message)).SingleLine().Grow(1)
			ui.Text(c, ago(now, e.Time)).FontSize(12).Width(88).TextColor(th.TextMuted).SingleLine()
			ui.Text(c, hex.EncodeToString(e.OID)[:8]).Font("monospace").FontSize(12).Width(64).TextColor(th.TextMuted)
		})
		row.ContextMenu(func(m *ui.Menu) { a.commitActions(m, t, e.OID) })
	}).Grow(1)
	if rs.chosen >= 0 && rs.chosen < len(entries) {
		rs.chosenID = entries[rs.chosen].OID
	}
}

func (a *App) lostList(c *ui.Context, t *tab) {
	th := c.Theme()
	rs := &t.reflog
	if !rs.lostSearched {
		ui.Column(c).Fill().Center().Gap(12).Children(func() {
			if ui.Button(c, "Find Lost Commits").Clicked() {
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
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	case rs.lostErr != "":
		ui.Text(c, rs.lostErr).TextColor(th.Danger).Padding(12).Selectable()
		return
	case len(rs.lost) == 0:
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "No lost commits").TextColor(th.TextMuted)
		})
		return
	}
	lost := rs.lost
	rs.lostList.Selected = &rs.lostChosen
	rs.lostList.Label = func(i int) string { return redact.Control(lost[i].Subject) + ", " + lost[i].Author }
	now := a.Now()
	ui.List(c, &rs.lostList, len(lost), func(i int) {
		l := lost[i]
		row := ui.Row(c).Padding(3, 12).Gap(8).AlignItems(ui.Center).FillWidth()
		row.Children(func() {
			ui.Text(c, redact.Control(l.Subject)).SingleLine().Grow(1)
			ui.Text(c, redact.Control(l.Author)).FontSize(12).Width(120).TextColor(th.TextMuted).SingleLine()
			ui.Text(c, ago(now, l.Time)).FontSize(12).Width(88).TextColor(th.TextMuted).SingleLine()
			ui.Text(c, hex.EncodeToString(l.OID)[:8]).Font("monospace").FontSize(12).Width(64).TextColor(th.TextMuted)
		})
		row.ContextMenu(func(m *ui.Menu) { a.commitActions(m, t, l.OID) })
	}).Grow(1)
}

// ago says how long before now t was, roughly.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	case d < 60*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day") + " ago"
	}
	return t.Format("2006-01-02")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	if w, ok := strings.CutSuffix(unit, "y"); ok && w != "" && !strings.ContainsRune("aeiou", rune(w[len(w)-1])) {
		return strconv.Itoa(n) + " " + w + "ies"
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
