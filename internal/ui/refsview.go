package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/write"
	"ramus/internal/session"
)

// refsState is a tab's view of its branches, tags and remotes.
type refsState struct {
	kind   int // refsBranches, refsTags, refsRemotes
	filter string
	// from is the sidebar ref the view was last opened at.
	from string

	info    *session.RefInfo
	readFor *refs.List
	readW   int
	loading bool

	closed map[string]bool
	// closedGen counts folders opened and closed, for the rows key.
	closedGen int
	rows      []refRow
	rowsFor   rowsKey
	// rowsAsked is the key of rows being grouped off the main thread.
	rowsAsked rowsKey
	list      ui.ListState
	chosen    int
	// byName indexes the branches and tags of byNameFor by full name.
	byName    map[string]int
	byNameFor *session.RefInfo
	// chosenName keeps the row chosen as rows come and go.
	chosenName string
	reveal     bool

	prune bool
	ask   confirmState
	// switching is the branch asked to be checked out over local changes.
	switching string
	// deleted are the branches last deleted here, until restored.
	deleted []session.BranchInfo
	dlg     refDialog
	clean   cleanupState
}

const (
	refsBranches = iota
	refsTags
	refsRemotes
)

type rowsKey struct {
	info   *session.RefInfo
	kind   int
	filter string
	gen    int
}

// refDialog asks for the names or URL a write needs.
type refDialog struct {
	open   bool
	title  string
	labels []string
	values []string
	// tag shows the kinds of tag and a message.
	tag     bool
	kind    int
	message string
	button  string
	run     func(d *refDialog) (write.Op, error)
	err     string
}

// cleanupState lists the branches that can go, to delete those picked.
type cleanupState struct {
	open    bool
	base    string
	loading bool
	err     string
	found   []session.Candidate
	picked  []bool
	list    ui.ListState
}

func newRefsState() refsState {
	return refsState{chosen: -1, closed: map[string]bool{}, prune: true}
}

// showRef opens the view at a ref chosen in the sidebar.
func (rs *refsState) showRef(name string) {
	if name == rs.from {
		return
	}
	rs.from = name
	rs.kind = refsBranches
	if strings.HasPrefix(name, "refs/tags/") {
		rs.kind = refsTags
	}
	rs.chosenName, rs.filter, rs.reveal = name, "", true
}

// loadRefs reads the refs again after a ref or a write changed them.
func (a *App) loadRefs(t *tab) {
	rs := &t.refs
	st := t.s.State
	if rs.loading || (st.Refs == rs.readFor && t.s.Writes == rs.readW && rs.info != nil) {
		return
	}
	rs.readFor, rs.readW, rs.loading = st.Refs, t.s.Writes, true
	t.s.LoadRefInfo(func(info session.RefInfo) {
		rs.loading = false
		rs.info = &info
	})
}

func (a *App) refsView(c *ui.Context, t *tab) {
	th := c.Theme()
	rs := &t.refs
	a.loadRefs(t)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Segmented(c, &rs.kind, "Branches", "Tags", "Remotes")
			if rs.kind != refsRemotes {
				ui.TextInput(c, &rs.filter).Placeholder("Filter").Label("Filter").Width(200)
			}
			ui.Spacer(c)
			a.remoteMenus(c, t)
			ro := t.s.Queue == nil
			switch rs.kind {
			case refsBranches:
				if d := rs.deleted; len(d) > 0 {
					what := "Deleted " + plural(len(d), "branch")
					if len(d) == 1 {
						what = "Deleted " + redact.Control(strings.TrimPrefix(d[0].Name, "refs/heads/")) + " at " + d[0].Commit[:min(10, len(d[0].Commit))]
					}
					ui.Text(c, what).FontSize(12).TextColor(th.TextMuted).SingleLine()
					if ui.Button(c, "Restore").Disabled(ro).Clicked() {
						a.restoreBranches(t, d)
					}
				}
				if ui.Button(c, "Clean Up…").Disabled(rs.info == nil).Clicked() {
					a.openCleanup(t)
				}
			case refsTags:
				if ui.Button(c, "New Tag…").Disabled(ro || len(t.s.State.Head.OID) == 0).Clicked() {
					a.tagDialog(t, "HEAD")
				}
			case refsRemotes:
				if ui.Button(c, "Add Remote…").Disabled(ro).Clicked() {
					rs.dlg = refDialog{open: true, title: "Add Remote", labels: []string{"Name", "URL"}, values: []string{"", ""}, button: "Add",
						run: func(d *refDialog) (write.Op, error) {
							return write.AddRemote(t.s.Where(), strings.TrimSpace(d.values[0]), strings.TrimSpace(d.values[1]))
						}}
				}
			}
		})
		switch {
		case rs.info == nil:
			ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			return
		case rs.info.Err != nil:
			ui.Text(c, errText(rs.info.Err)).TextColor(th.Danger).Padding(12).Selectable()
			return
		}
		switch rs.kind {
		case refsBranches, refsTags:
			a.refList(c, t)
		case refsRemotes:
			a.remoteList(c, t)
		}
	})
	a.confirmView(c, &rs.ask)
	a.switchAsk(c, t)
	a.refDialogView(c, &rs.dlg, t)
	a.cleanupView(c, t)
}

// refNames are the branch or tag names shown, without their prefix.
func refNames(info *session.RefInfo, kind int) []string {
	var out []string
	if kind == refsBranches {
		for _, b := range info.Branches {
			out = append(out, strings.TrimPrefix(b.Name, "refs/heads/"))
		}
		return out
	}
	for _, tg := range info.Tags {
		out = append(out, strings.TrimPrefix(tg.Name, "refs/tags/"))
	}
	return out
}

// closedPrefix keys a list's closed folders in refsState.closed.
func closedPrefix(kind int) string {
	if kind == refsTags {
		return "t:"
	}
	return "b:"
}

func (a *App) refList(c *ui.Context, t *tab) {
	th := c.Theme()
	rs := &t.refs
	info := rs.info
	key := rowsKey{info, rs.kind, rs.filter, rs.closedGen}
	if key != rs.rowsFor && key != rs.rowsAsked {
		// Grouped off the main thread: tens of thousands of refs take
		// milliseconds. The rows shown stay until the new ones come.
		rs.rowsAsked = key
		// The folders closed in this list, by path, as groupRefs takes them.
		closed, update := map[string]bool{}, a.Update
		for k, v := range rs.closed {
			if folder, ok := strings.CutPrefix(k, closedPrefix(rs.kind)); ok {
				closed[folder] = v
			}
		}
		go func() {
			rows := groupRefs(refNames(key.info, key.kind), key.filter, closed)
			update(func() {
				if rs.rowsAsked != key {
					return
				}
				rs.rows, rs.rowsFor = rows, key
				rs.chosen = -1
				for i, r := range rows {
					if r.idx >= 0 && fullRef(key.info, key.kind, r.idx) == rs.chosenName {
						rs.chosen = i
						if rs.reveal {
							rs.list.ScrollTo(i, ui.Center)
						}
					}
				}
				rs.reveal = false
			})
		}()
	}
	if rs.rowsFor.info == nil || rs.rowsFor.kind != rs.kind {
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	}
	// The rows index the ref details they were grouped from, which stay
	// shown until the rows of newer ones come.
	info = rs.rowsFor.info
	rows := rs.rows
	if len(rows) == 0 {
		ui.Column(c).Fill().Center().Children(func() {
			what := "No branches"
			if rs.kind == refsTags {
				what = "No tags"
			}
			if rs.filter != "" {
				what += " match"
			}
			ui.Text(c, what).TextColor(th.TextMuted)
		})
		return
	}
	prefix := closedPrefix(rs.kind)
	head := t.s.State.Head
	now := a.Now()
	rs.list.Selected = &rs.chosen
	rs.list.Label = func(i int) string {
		if rows[i].idx < 0 {
			return rows[i].folder + "/"
		}
		return redact.Control(rows[i].label)
	}
	var act func()
	ui.List(c, &rs.list, len(rows), func(i int) {
		r := rows[i]
		row := ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).FillWidth()
		row.Children(func() {
			if r.idx < 0 {
				ui.Box(c).Width(float32(r.depth) * 16)
				k := prefix + r.folder
				arrow := "▾"
				if rs.closed[k] && rs.filter == "" {
					arrow = "▸"
				}
				if ui.Text(c, arrow).Width(12).TextColor(th.TextMuted).RowClicked() {
					act = func() { rs.closed[k] = !rs.closed[k]; rs.closedGen++ }
				}
				ui.Text(c, redact.Control(r.label)+"/").SingleLine().Grow(1)
				return
			}
			// The name column is as wide at every depth, so the columns
			// after it line up.
			var b session.BranchInfo
			if rs.kind == refsBranches {
				b = info.Branches[r.idx]
			}
			ui.Row(c).Width(nameColumn).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Width(float32(r.depth)*16 + 20)
				name := ui.Text(c, redact.Control(r.label)).SingleLine().Grow(1)
				if rs.kind == refsBranches && !head.Detached && b.Name == head.Target {
					name.Bold()
				}
				if b.Worktree != "" {
					ui.Text(c, "in "+filepath.Base(b.Worktree)).FontSize(12).TextColor(th.TextMuted).SingleLine().Description("Checked out in " + b.Worktree)
				}
				if on := stackedOn(info, b.Name); on != "" {
					ui.Text(c, "on "+redact.Control(on)).FontSize(12).TextColor(th.TextMuted).SingleLine().Description("Stacked on " + redact.Control(on))
				}
			})
			if rs.kind == refsBranches {
				tr := ui.Text(c, tracking(b)).FontSize(12).Width(180).SingleLine()
				if b.Gone {
					tr.TextColor(th.Warning)
				} else {
					tr.TextColor(th.TextMuted)
				}
				ui.Text(c, redact.Control(b.Subject)).FontSize(12).TextColor(th.TextMuted).SingleLine().Grow(1)
				ui.Text(c, ago(now, time.Unix(b.Time, 0))).FontSize(12).Width(88).TextColor(th.TextMuted).SingleLine()
				return
			}
			tg := info.Tags[r.idx]
			kind := "lightweight"
			if tg.Annotated {
				kind = "annotated"
			}
			ui.Text(c, kind).FontSize(12).Width(88).TextColor(th.TextMuted).SingleLine()
			ui.Text(c, redact.Control(tg.Subject)).FontSize(12).TextColor(th.TextMuted).SingleLine().Grow(1)
			ui.Text(c, ago(now, time.Unix(tg.Time, 0))).FontSize(12).Width(88).TextColor(th.TextMuted).SingleLine()
		})
		if r.idx < 0 {
			return
		}
		if rs.kind == refsBranches {
			b := info.Branches[r.idx]
			if row.RowDoubleClicked() {
				act = func() { a.checkOut(t, b) }
			}
			row.ContextMenu(func(m *ui.Menu) { a.branchMenu(m, t, b, &act) })
			return
		}
		tg := info.Tags[r.idx]
		row.ContextMenu(func(m *ui.Menu) { a.tagMenu(m, t, tg, &act) })
	}).Label("Branches, tags and remotes").Grow(1)
	if rs.chosen >= 0 && rs.chosen < len(rows) && rows[rs.chosen].idx >= 0 {
		rs.chosenName = fullRef(info, rs.kind, rows[rs.chosen].idx)
	}
	if act != nil {
		act()
	}
}

// nameColumn is the width of the branch and tag name column.
const nameColumn = 340

// sidebarRefMenu gives a branch or tag in the sidebar the menu it has in
// the branches view, once the refs are read.
func (a *App) sidebarRefMenu(it ui.Element, t *tab, name string) {
	if strings.HasPrefix(name, "refs/remotes/") {
		it.ContextMenu(func(m *ui.Menu) {
			var act func()
			a.historyRefItems(m, t, name, &act)
			if act != nil {
				act()
			}
		})
		return
	}
	if !strings.HasPrefix(name, "refs/heads/") && !strings.HasPrefix(name, "refs/tags/") {
		return
	}
	a.loadRefs(t)
	rs := &t.refs
	if rs.info == nil {
		return
	}
	if rs.byNameFor != rs.info {
		rs.byNameFor, rs.byName = rs.info, map[string]int{}
		for i, b := range rs.info.Branches {
			rs.byName[b.Name] = i
		}
		for i, tg := range rs.info.Tags {
			rs.byName[tg.Name] = i
		}
	}
	i, ok := rs.byName[name]
	if !ok {
		return
	}
	info := rs.info
	it.ContextMenu(func(m *ui.Menu) {
		var act func()
		if strings.HasPrefix(name, "refs/heads/") {
			a.branchMenu(m, t, info.Branches[i], &act)
		} else {
			a.tagMenu(m, t, info.Tags[i], &act)
		}
		if act != nil {
			act()
		}
	})
}

func fullRef(info *session.RefInfo, kind, idx int) string {
	if kind == refsBranches {
		return info.Branches[idx].Name
	}
	return info.Tags[idx].Name
}

// stackedOn is the branch name is stacked on, short, or "".
func stackedOn(info *session.RefInfo, name string) string {
	for _, s := range info.Stacks {
		if i := slices.Index(s, name); i > 0 {
			return strings.TrimPrefix(s[i-1], "refs/heads/")
		}
	}
	return ""
}

// tracking says where a branch is against its upstream.
func tracking(b session.BranchInfo) string {
	if b.Upstream == "" {
		return ""
	}
	up := strings.TrimPrefix(b.Upstream, "refs/remotes/")
	switch {
	case b.Gone:
		return up + " gone"
	case b.Ahead == 0 && b.Behind == 0:
		return up
	}
	s := up
	if b.Ahead > 0 {
		s += " ↑" + strconv.Itoa(b.Ahead)
	}
	if b.Behind > 0 {
		s += " ↓" + strconv.Itoa(b.Behind)
	}
	return s
}

// branchMenu is what can be done with a local branch.
func (a *App) branchMenu(m *ui.Menu, t *tab, b session.BranchInfo, act *func()) {
	ro := t.s.Queue == nil
	head := t.s.State.Head
	current := !head.Detached && head.Target == b.Name
	short := strings.TrimPrefix(b.Name, "refs/heads/")
	w, seen := t.s.Where(), t.s.Seen()
	if m.Item("Check Out").Disabled(ro || current || b.Worktree != "").Chosen() {
		*act = func() { a.checkOut(t, b) }
	}
	into := "HEAD"
	if !head.Detached {
		into = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	if m.Item("Merge into " + redact.Control(into) + "…").Disabled(ro || current).Chosen() {
		*act = func() { a.openMerge(t, b.Name) }
	}
	if m.Item("Rebase " + redact.Control(into) + " onto This").Disabled(ro || current || head.Detached).Chosen() {
		*act = func() {
			op, err := write.Rebase(w, b.Name, seen)
			a.submit(t, op, err)
		}
	}
	if info := t.refs.info; info != nil {
		if st := info.StackOf(b.Name); len(st) > 1 {
			if m.Item(fmt.Sprintf("Push Stack of %d Branches", len(st))).Disabled(ro).Chosen() {
				*act = func() {
					if _, err := t.s.PushStack(st); err != nil {
						a.notice = errText(err)
					}
				}
			}
		}
	}
	m.Separator()
	if m.Item("Create Tag Here…").Disabled(ro).Chosen() {
		*act = func() { a.tagDialog(t, b.Commit) }
	}
	if m.Item("Rename…").Disabled(ro).Chosen() {
		*act = func() {
			t.refs.dlg = refDialog{open: true, title: "Rename " + redact.Control(short), labels: []string{"New name"}, values: []string{short}, button: "Rename",
				run: func(d *refDialog) (write.Op, error) {
					return write.RenameBranch(w, b.Name, strings.TrimSpace(d.values[0]), seen)
				}}
		}
	}
	if m.Item("Delete…").Disabled(ro || current).Danger().Chosen() {
		*act = func() { a.deleteBranches(t, []session.BranchInfo{b}) }
	}
	m.Separator()
	if m.Item("Copy Name").Chosen() {
		a.clip = short
	}
	m.Separator()
	pin := "Pin to Top"
	if slices.Contains(t.prefs.pinned, b.Name) {
		pin = "Unpin"
	}
	if m.Item(pin).Disabled(!t.prefs.loaded).Chosen() {
		*act = func() { a.toggleRef(t, &t.prefs.pinned, b.Name) }
	}
	a.historyRefItems(m, t, b.Name, act)
}

// historyRefItems hide a ref from history, show it again, or show it
// alone.
func (a *App) historyRefItems(m *ui.Menu, t *tab, name string, act *func()) {
	hide := "Hide in History"
	if slices.Contains(t.prefs.hidden, name) {
		hide = "Show in History"
	}
	if m.Item(hide).Disabled(!t.prefs.loaded).Chosen() {
		*act = func() { a.toggleRef(t, &t.prefs.hidden, name) }
	}
	if m.Item("Show Only This in History").Chosen() {
		*act = func() {
			hf := &t.hist.filter
			hf.only, hf.open = name, true
			if i := slices.Index(t.prefs.hidden, name); i >= 0 {
				a.toggleRef(t, &t.prefs.hidden, name)
			} else {
				a.applyHistoryFilter(t)
			}
			t.sel = "history"
		}
	}
}

// checkOut switches to a branch, asking first what to do with local
// changes when there are some.
func (a *App) checkOut(t *tab, b session.BranchInfo) {
	if t.s.Queue == nil || b.Worktree != "" {
		return
	}
	if st := t.s.State.Status; st != nil && st.Len() > 0 {
		t.refs.switching = b.Name
		return
	}
	a.submit(t, write.SwitchWith(t.s.Where(), b.Name, write.CarryChanges, t.s.Seen()), nil)
}

func (a *App) switchAsk(c *ui.Context, t *tab) {
	rs := &t.refs
	if rs.switching == "" {
		return
	}
	open := true
	name := redact.Control(strings.TrimPrefix(rs.switching, "refs/heads/"))
	choice := ui.AlertDialog(c, &open, "Switch to "+name+" with local changes?",
		"Carried changes stay in the files; git stops if one would be overwritten. Stashed changes go to the stash list.",
		"Carry Them", "Stash Them", "Cancel")
	branch := rs.switching
	if !open {
		rs.switching = ""
	}
	switch choice {
	case 0:
		a.submit(t, write.SwitchWith(t.s.Where(), branch, write.CarryChanges, t.s.Seen()), nil)
	case 1:
		a.submit(t, write.SwitchWith(t.s.Where(), branch, write.StashChanges, t.s.Seen()), nil)
	}
}

// deleteBranches asks, naming each branch's commit, then deletes them,
// each with the worktree that has it. Each is backed up first, and each
// delete is undone on its own.
func (a *App) deleteBranches(t *tab, bs []session.BranchInfo) {
	if len(bs) == 0 {
		return
	}
	title := "Delete " + plural(len(bs), "branch") + "?"
	if len(bs) == 1 {
		b := bs[0]
		title = "Delete branch " + redact.Control(strings.TrimPrefix(b.Name, "refs/heads/")) + " at " + b.Commit[:min(10, len(b.Commit))] + "?"
	}
	msg := "Each is backed up first; Undo brings it back."
	var wts []string
	for _, b := range bs {
		if b.Worktree != "" {
			wts = append(wts, b.Worktree)
		}
	}
	if len(wts) > 0 {
		msg += " The worktree at " + strings.Join(wts, ", ") + " is removed with its branch; git refuses if it has changes."
	}
	w, seen := t.s.Where(), t.s.Seen()
	t.refs.ask = confirmState{open: true, title: title, message: msg, button: "Delete", run: func() {
		for _, b := range bs {
			a.submit(t, write.DeleteBranch(w, b.Name, b.Commit, true, b.Worktree, seen), nil)
		}
		t.refs.deleted = bs
	}}
}

// restoreBranches makes deleted branches again at the commits they were
// at; a worktree removed with one is not made again.
func (a *App) restoreBranches(t *tab, bs []session.BranchInfo) {
	w, seen := t.s.Where(), t.s.Seen()
	for _, b := range bs {
		op, err := write.CreateBranch(w, b.Name, b.Commit, false, seen)
		op.Name = "Restore branch " + strings.TrimPrefix(b.Name, "refs/heads/") + " at " + b.Commit[:min(12, len(b.Commit))]
		a.submit(t, op, err)
	}
	t.refs.deleted = nil
}

func (a *App) tagMenu(m *ui.Menu, t *tab, tg session.TagInfo, act *func()) {
	ro := t.s.Queue == nil
	short := strings.TrimPrefix(tg.Name, "refs/tags/")
	if m.Item("Check Out").Disabled(ro || tg.Commit == "").Chosen() {
		*act = func() { a.submit(t, write.Detach(t.s.Where(), tg.Commit, t.s.Seen()), nil) }
	}
	var remotes []session.RemoteInfo
	if t.refs.info != nil {
		remotes = t.refs.info.Remotes
	}
	m.Submenu("Push to", func(sub *ui.Menu) {
		for _, r := range remotes {
			if sub.Item(redact.Control(r.Name)).Disabled(ro).Chosen() {
				*act = func() {
					op, err := write.PushTag(t.s.Where(), r.Name, short)
					a.submit(t, op, err)
				}
			}
		}
	}).Disabled(len(remotes) == 0)
	if m.Item("Delete…").Disabled(ro).Danger().Chosen() {
		*act = func() {
			t.refs.ask = confirmState{open: true, title: "Delete tag " + redact.Control(short) + " at " + tg.Object[:min(10, len(tg.Object))] + "?",
				message: "Only here; a tag already pushed stays on the remote. Undo brings it back.", button: "Delete",
				run: func() { a.submit(t, write.DeleteTag(t.s.Where(), short, tg.Object), nil) }}
		}
	}
	m.Separator()
	if m.Item("Copy Name").Chosen() {
		a.clip = short
	}
}

// tagDialog asks for a tag to make at commit.
func (a *App) tagDialog(t *tab, commit string) {
	shown := commit
	if len(shown) > 10 {
		shown = shown[:10]
	}
	t.refs.dlg = refDialog{open: true, title: "Create Tag at " + shown, labels: []string{"Tag name"}, values: []string{""}, tag: true, kind: 1, button: "Create",
		run: func(d *refDialog) (write.Op, error) {
			kinds := []write.TagKind{write.Lightweight, write.Annotated, write.Signed}
			return write.CreateTag(t.s.Where(), strings.TrimSpace(d.values[0]), commit, kinds[d.kind], d.message)
		}}
}

func (a *App) refDialogView(c *ui.Context, d *refDialog, t *tab) {
	if !d.open {
		return
	}
	th := c.Theme()
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(460).Gap(12).Padding(20).Children(func() {
			ui.Text(c, d.title).Bold()
			submitted := false
			for i, l := range d.labels {
				in := ui.TextInput(c, &d.values[i]).Placeholder(l).Label(l)
				if i == 0 {
					in.AutoFocus()
				}
				if in.Submitted() && !d.tag {
					submitted = true
				}
			}
			if d.tag {
				ui.Segmented(c, &d.kind, "Lightweight", "Annotated", "Signed")
				if d.kind > 0 {
					ui.TextArea(c, &d.message).Placeholder("Message").Label("Message").Height(96)
				}
			}
			if d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(th.Danger).Selectable()
			}
			empty := false
			for _, v := range d.values {
				empty = empty || strings.TrimSpace(v) == ""
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if (ui.PrimaryButton(c, d.button).Disabled(empty).Clicked() || submitted && !empty) && d.run != nil {
					op, err := d.run(d)
					if err == nil {
						_, err = t.s.Submit(op)
					}
					if err != nil {
						d.err = errText(err)
						return
					}
					d.open = false
				}
			})
		})
	})
}

// remoteMenus are fetch, pull and push.
func (a *App) remoteMenus(c *ui.Context, t *tab) {
	rs := &t.refs
	ro := t.s.Queue == nil
	var remotes []session.RemoteInfo
	var cur *session.BranchInfo
	head := t.s.State.Head
	if rs.info != nil {
		remotes = rs.info.Remotes
		for i, b := range rs.info.Branches {
			if !head.Detached && b.Name == head.Target {
				cur = &rs.info.Branches[i]
			}
		}
	}
	none := ro || len(remotes) == 0
	w := t.s.Where()
	ui.PopoverMenuButton(c, "Fetch", func(m *ui.Menu) {
		if m.Item("Fetch All Remotes").Disabled(none).Chosen() {
			op, err := write.Fetch(w, "", rs.prune)
			a.submit(t, op, err)
		}
		for _, r := range remotes {
			if m.Item("Fetch " + redact.Control(r.Name)).Disabled(none).Chosen() {
				op, err := write.Fetch(w, r.Name, rs.prune)
				a.submit(t, op, err)
			}
		}
		m.Separator()
		if m.Item("Prune Deleted Branches").Checked(rs.prune).Chosen() {
			rs.prune = !rs.prune
		}
	})
	noUp := none || cur == nil || cur.Upstream == "" || cur.Gone
	ui.PopoverMenuButton(c, "Pull", func(m *ui.Menu) {
		for _, mode := range []struct {
			label string
			mode  write.PullMode
		}{
			{"Pull, Merging", write.PullMerge},
			{"Pull, Rebasing", write.PullRebase},
			{"Pull, Fast-Forward Only", write.PullFFOnly},
		} {
			if m.Item(mode.label).Disabled(noUp).Chosen() {
				op, err := write.Pull(w, mode.mode, t.s.Seen())
				a.submit(t, op, err)
			}
		}
	})
	ui.PopoverMenuButton(c, "Push", func(m *ui.Menu) {
		if m.Item("Push").Disabled(noUp).Chosen() {
			a.submit(t, write.Push(w, write.PushOptions{}), nil)
		}
		for _, r := range remotes {
			if m.Item("Push to " + redact.Control(r.Name) + " and Track").Disabled(none || cur == nil).Chosen() {
				a.submit(t, write.Push(w, write.PushOptions{Remote: r.Name}), nil)
			}
		}
		m.Separator()
		if m.Item("Force Push with Lease…").Disabled(noUp).Danger().Chosen() {
			up := strings.TrimPrefix(cur.Upstream, "refs/remotes/")
			rs.ask = confirmState{open: true, title: "Overwrite " + redact.Control(up) + "?",
				message: "Only if it is still where this repository last fetched it; otherwise nothing is pushed.", button: "Force Push",
				run: func() { a.submit(t, write.Push(w, write.PushOptions{ForceWithLease: true}), nil) }}
		}
	})
}

func (a *App) remoteList(c *ui.Context, t *tab) {
	th := c.Theme()
	rs := &t.refs
	remotes := rs.info.Remotes
	if len(remotes) == 0 {
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "No remotes").TextColor(th.TextMuted)
		})
		return
	}
	ro := t.s.Queue == nil
	w := t.s.Where()
	var act func()
	ui.Column(c).Grow(1).Children(func() {
		for _, r := range remotes {
			row := ui.Row(c).Padding(6, 12).Gap(12).AlignItems(ui.Center).FillWidth().Key(r.Name)
			row.Children(func() {
				ui.Text(c, redact.Control(r.Name)).Bold().Width(160).SingleLine()
				ui.Column(c).Grow(1).Children(func() {
					ui.Text(c, redact.Shown(r.URL)).FontSize(12).SingleLine().Selectable()
					if r.PushURL != "" && r.PushURL != r.URL {
						ui.Text(c, "push: "+redact.Shown(r.PushURL)).FontSize(12).TextColor(th.TextMuted).SingleLine().Selectable()
					}
				})
			})
			row.ContextMenu(func(m *ui.Menu) {
				if m.Item("Fetch").Disabled(ro).Chosen() {
					act = func() {
						op, err := write.Fetch(w, r.Name, rs.prune)
						a.submit(t, op, err)
					}
				}
				if m.Item("Rename…").Disabled(ro).Chosen() {
					act = func() {
						rs.dlg = refDialog{open: true, title: "Rename Remote " + redact.Control(r.Name), labels: []string{"New name"}, values: []string{r.Name}, button: "Rename",
							run: func(d *refDialog) (write.Op, error) {
								return write.RenameRemote(w, r.Name, strings.TrimSpace(d.values[0]))
							}}
					}
				}
				if m.Item("Change URL…").Disabled(ro).Chosen() {
					act = func() {
						// A URL with a credential in it is not put back in
						// the field, where it would show.
						url := r.URL
						if redact.Secrets(url) != url {
							url = ""
						}
						rs.dlg = refDialog{open: true, title: "URL of " + redact.Control(r.Name), labels: []string{"URL"}, values: []string{url}, button: "Change",
							run: func(d *refDialog) (write.Op, error) {
								return write.SetRemoteURL(w, r.Name, strings.TrimSpace(d.values[0]))
							}}
					}
				}
				m.Separator()
				if m.Item("Remove…").Disabled(ro).Danger().Chosen() {
					act = func() {
						rs.ask = confirmState{open: true, title: "Remove remote " + redact.Control(r.Name) + "?",
							message: "Its remote-tracking branches go with it; the remote itself is not touched.", button: "Remove",
							run: func() {
								op, err := write.RemoveRemote(w, r.Name)
								a.submit(t, op, err)
							}}
					}
				}
			})
		}
	})
	if act != nil {
		act()
	}
}

// openCleanup looks for branches that can go, against the branch the
// current one most likely merges into.
func (a *App) openCleanup(t *tab) {
	cs := &t.refs.clean
	*cs = cleanupState{open: true, base: cleanupBase(t)}
	a.findCleanup(t)
}

// cleanupBase is main or master when there is one, else the current
// branch.
func cleanupBase(t *tab) string {
	if info := t.refs.info; info != nil {
		for _, want := range []string{"refs/heads/main", "refs/heads/master", "refs/heads/trunk"} {
			for _, b := range info.Branches {
				if b.Name == want {
					return want
				}
			}
		}
	}
	return t.s.State.Head.Target
}

func (a *App) findCleanup(t *tab) {
	cs := &t.refs.clean
	info := t.refs.info
	if info == nil || cs.base == "" {
		return
	}
	cs.loading, cs.found, cs.picked, cs.err = true, nil, nil, ""
	base := cs.base
	current := ""
	if h := t.s.State.Head; !h.Detached {
		current = h.Target
	}
	t.s.FindCleanup(base, current, info.Branches, func(found []session.Candidate, err error) {
		if base != cs.base {
			return
		}
		cs.loading, cs.found = false, found
		cs.picked = make([]bool, len(found))
		for i := range cs.picked {
			cs.picked[i] = true
		}
		if err != nil {
			cs.err = errText(err)
		}
	})
}

// whyWords says why a branch can go.
func whyWords(w session.Why) string {
	var out []string
	for _, x := range []struct {
		bit  session.Why
		word string
	}{
		{session.Merged, "merged"},
		{session.RebaseMerged, "rebased in"},
		{session.SquashMerged, "squashed in"},
		{session.UpstreamGone, "upstream gone"},
	} {
		if w&x.bit != 0 {
			out = append(out, x.word)
		}
	}
	return strings.Join(out, ", ")
}

func (a *App) cleanupView(c *ui.Context, t *tab) {
	cs := &t.refs.clean
	if !cs.open {
		return
	}
	th := c.Theme()
	ui.Modal(c, &cs.open, func() {
		ui.Column(c).Width(640).Height(480).Gap(12).Padding(20).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Branches That Can Go").Bold().Grow(1)
				ui.Text(c, "Against").TextColor(th.TextMuted)
				ui.PopoverMenuButton(c, redact.Control(strings.TrimPrefix(cs.base, "refs/heads/")), func(m *ui.Menu) {
					if t.refs.info == nil {
						return
					}
					for i, b := range t.refs.info.Branches {
						if i == sideLimit {
							break
						}
						if m.Item(redact.Control(strings.TrimPrefix(b.Name, "refs/heads/"))).Radio(b.Name == cs.base).Chosen() {
							cs.base = b.Name
							a.findCleanup(t)
						}
					}
				})
			})
			switch {
			case cs.loading:
				ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c) })
			case cs.err != "":
				ui.Text(c, cs.err).TextColor(th.Danger).Selectable().Grow(1)
			case len(cs.found) == 0:
				ui.Column(c).Grow(1).Center().Children(func() {
					ui.Text(c, "No branches can go").TextColor(th.TextMuted)
				})
			default:
				found := cs.found
				cs.list.Label = func(i int) string { return strings.TrimPrefix(found[i].Branch.Name, "refs/heads/") }
				ui.List(c, &cs.list, len(found), func(i int) {
					f := found[i]
					ui.Row(c).Padding(3, 4).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
						ui.Checkbox(c, &cs.picked[i], redact.Control(strings.TrimPrefix(f.Branch.Name, "refs/heads/"))).Grow(1)
						if f.Branch.Worktree != "" {
							ui.Text(c, "in "+filepath.Base(f.Branch.Worktree)).FontSize(12).TextColor(th.TextMuted).SingleLine()
						}
						ui.Text(c, whyWords(f.Why)).FontSize(12).TextColor(th.TextMuted).Width(200).SingleLine()
					})
				}).Label("Branches to clean up").Grow(1)
			}
			n := 0
			for _, p := range cs.picked {
				if p {
					n++
				}
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					cs.open = false
				}
				label := "Delete"
				if n > 0 {
					label = "Delete " + plural(n, "Branch")
				}
				if ui.PrimaryButton(c, label).Disabled(n == 0 || t.s.Queue == nil || cs.loading).Clicked() {
					var bs []session.BranchInfo
					for i, f := range cs.found {
						if cs.picked[i] {
							bs = append(bs, f.Branch)
						}
					}
					cs.open = false
					a.deleteBranches(t, bs)
				}
			})
		})
	})
}
