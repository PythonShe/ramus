package ui

import (
	"bytes"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/write"
	"ramus/internal/session"
)

// historyEdit is the editor of a run of history: its commits, oldest
// first as git replays them, each with what to do with it.
type historyEdit struct {
	open   bool
	t      *tab
	rw     write.Rewrite
	rows   []editRow
	list   ui.ListState
	chosen int
	err    string
}

type editRow struct {
	session.EditCommit
	action   write.Action
	message  string
	position int // where git had it
	// loaded is set once message holds the commit's own message.
	loaded bool
}

// actionLabels name the actions in the order of write.Action.
var actionLabels = [...]string{"Pick", "Reword", "Edit", "Squash", "Fix Up", "Drop"}

// editHistory opens the editor of the history from commit id to HEAD,
// its rows as prefill makes them when it is not nil.
func (a *App) editHistory(t *tab, id []byte, prefill func(rows []editRow) ([]editRow, error)) {
	t.s.EditRange(id, func(rw write.Rewrite, commits []session.EditCommit, err error) {
		if err != nil {
			a.notice = errText(err)
			return
		}
		rows := make([]editRow, len(commits))
		for i, c := range commits {
			rows[i] = editRow{EditCommit: c, position: i}
		}
		if prefill != nil {
			if rows, err = prefill(rows); err != nil {
				a.notice = errText(err)
				return
			}
		}
		a.history = historyEdit{open: true, t: t, rw: rw, rows: rows, chosen: -1}
	})
}

// steps are the rows as the steps of the edit.
func (he *historyEdit) steps() []write.Step {
	steps := make([]write.Step, len(he.rows))
	for i, r := range he.rows {
		s := write.Step{Action: r.action, Commit: hex.EncodeToString(r.OID), Refs: r.Branches}
		if r.action == write.DoReword || r.action == write.DoSquash {
			s.Message = r.message
		}
		steps[i] = s
	}
	return steps
}

// changed reports whether the edit does anything.
func (he *historyEdit) changed() bool {
	for i, r := range he.rows {
		if r.action != write.DoPick || r.position != i {
			return true
		}
	}
	return false
}

// move puts rows before row to, keeping their order.
func (he *historyEdit) move(rows []int, to int) {
	take := map[int]bool{}
	for _, i := range rows {
		take[i] = true
	}
	var moved, rest []editRow
	at := 0
	for i, r := range he.rows {
		if i == to {
			at = len(rest)
		}
		if take[i] {
			moved = append(moved, r)
		} else {
			rest = append(rest, r)
		}
	}
	if to >= len(he.rows) {
		at = len(rest)
	}
	he.rows = append(append(append(make([]editRow, 0, len(he.rows)), rest[:at]...), moved...), rest[at:]...)
}

func (a *App) historyEditView(c *ui.Context) {
	he := &a.history
	if !he.open || he.t == nil {
		return
	}
	t := he.t
	th := c.Theme()
	ui.Modal(c, &he.open, func() {
		ui.Column(c).Width(760).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Edit History from "+he.rw.Commit[:min(10, len(he.rw.Commit))]).Bold()
			rows := he.rows
			he.list.Selected = &he.chosen
			he.list.Key = func(i int) any { return string(rows[i].OID) }
			he.list.Label = func(i int) string {
				return actionLabels[rows[i].action] + ", " + redact.Control(rows[i].Subject)
			}
			he.list.Reorder = he.move
			ui.List(c, &he.list, len(rows), func(i int) {
				r := &rows[i]
				ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
					ui.PopoverMenuButton(c, actionLabels[r.action], func(m *ui.Menu) {
						for k, label := range actionLabels {
							act := write.Action(k)
							if m.Item(label).Checked(r.action == act).Chosen() {
								a.setAction(he, i, act)
							}
						}
					}).Width(96)
					subject := ui.Text(c, redact.Control(r.Subject)).SingleLine().Grow(1)
					if r.action == write.DoDrop {
						subject.TextColor(th.TextMuted).Strikethrough()
					}
					for _, b := range r.Branches {
						ui.Text(c, strings.TrimPrefix(b, "refs/heads/")).FontSize(11).TextColor(th.TextMuted).SingleLine().MaxWidth(140)
					}
					ui.Text(c, hex.EncodeToString(r.OID)[:8]).Font("monospace").FontSize(12).Width(64).TextColor(th.TextMuted)
				})
			}).Label("Commits, oldest first").Height(min(float32(len(rows))*30+8, 360))
			if i := he.chosen; i >= 0 && i < len(rows) {
				switch r := &rows[i]; r.action {
				case write.DoReword:
					ui.TextArea(c, &r.message).Label("Message").Lines(3, 10).FillWidth()
				case write.DoSquash:
					ui.TextArea(c, &r.message).Label("Message").Placeholder("The messages, one after another").Lines(3, 10).FillWidth()
				}
			}
			if he.err != "" {
				ui.Text(c, he.err).FontSize(12).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					he.open = false
				}
				n := len(rows)
				if ui.PrimaryButton(c, "Rewrite "+countOf(n, "Commit")).Disabled(!he.changed()).Clicked() {
					op, err := write.EditHistory(t.s.Where(), he.rw, he.steps(), t.s.Seen())
					if err != nil {
						he.err = errText(err)
						return
					}
					he.open = false
					a.submitRewrite(t, op, nil)
				}
			})
		})
	})
}

// setAction gives row i action act; a reword starts from the commit's
// own message.
func (a *App) setAction(he *historyEdit, i int, act write.Action) {
	r := &he.rows[i]
	r.action, he.err, he.chosen = act, "", i
	if act != write.DoReword || r.loaded {
		return
	}
	id, t := r.OID, he.t
	a.withCommit(t, id, func(d session.CommitDetail) {
		for k := range he.rows {
			if row := &he.rows[k]; bytes.Equal(row.OID, id) && !row.loaded {
				row.loaded = true
				if row.message == "" {
					row.message = strings.TrimRight(string(d.Commit.Message), "\n")
				}
			}
		}
	})
}

// countOf is n things, with the plural made by adding s.
func countOf(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}

// squashPlan gathers the rows of folded (hex) right after the row of
// into, in their order, as squashes into it.
func squashPlan(into string, folded []string) func([]editRow) ([]editRow, error) {
	return func(rows []editRow) ([]editRow, error) {
		fold := map[string]bool{}
		for _, f := range folded {
			if f != into {
				fold[f] = true
			}
		}
		var moved, rest []editRow
		for _, r := range rows {
			if fold[hex.EncodeToString(r.OID)] {
				r.action = write.DoSquash
				moved = append(moved, r)
			} else {
				rest = append(rest, r)
			}
		}
		at := -1
		for i, r := range rest {
			if hex.EncodeToString(r.OID) == into {
				at = i
			}
		}
		if at < 0 || len(moved) != len(fold) {
			return nil, errors.New("the commits are not all on HEAD's line of first parents")
		}
		return append(append(append(make([]editRow, 0, len(rows)), rest[:at+1]...), moved...), rest[at+1:]...), nil
	}
}

// dropPlan marks the rows of dropped (hex) to be dropped.
func dropPlan(dropped []string) func([]editRow) ([]editRow, error) {
	return func(rows []editRow) ([]editRow, error) {
		n := 0
		for i := range rows {
			if slices.Contains(dropped, hex.EncodeToString(rows[i].OID)) {
				rows[i].action = write.DoDrop
				n++
			}
		}
		if n != len(dropped) {
			return nil, errors.New("the commits are not all on HEAD's line of first parents")
		}
		return rows, nil
	}
}

// rewriteMany adds what rewrites several commits picked (hex, newest
// first) to a menu; each opens the history editor with the plan.
func (a *App) rewriteMany(m *ui.Menu, t *tab, ids func() []string, n int) {
	ro := t.s.Queue == nil || t.s.Repo == nil
	m.Separator()
	open := func(plan func([]string) func([]editRow) ([]editRow, error)) {
		list := ids()
		oldest, _ := hex.DecodeString(list[len(list)-1])
		a.editHistory(t, oldest, plan(list))
	}
	if m.Item("Squash " + countOf(n, "Commit") + "…").Disabled(ro).Chosen() {
		open(func(list []string) func([]editRow) ([]editRow, error) { return squashPlan(list[len(list)-1], list) })
	}
	if m.Item("Drop " + countOf(n, "Commit") + "…").Disabled(ro).Danger().Chosen() {
		open(dropPlan)
	}
}

// draggedCommits are history rows dragged, by hex id, newest first.
type draggedCommits []string

// dropOnCommit opens the history editor with the commits dragged squashed
// into the commit into (hex), from whichever of them is oldest.
func (a *App) dropOnCommit(t *tab, h session.HistoryState, into string, dragged draggedCommits) {
	if len(dragged) == 0 || len(dragged) == 1 && dragged[0] == into {
		return
	}
	// The rows are newest first: the oldest of them is the last found.
	all := append(slices.Clone([]string(dragged)), into)
	oldest := ""
	for r := 0; r < h.Snap.Len(); r++ {
		if id := hex.EncodeToString(h.OID(h.Snap.Commit(r))); slices.Contains(all, id) {
			oldest = id
		}
	}
	from, err := hex.DecodeString(oldest)
	if err != nil || oldest == "" {
		return
	}
	a.editHistory(t, from, squashPlan(into, dragged))
}
