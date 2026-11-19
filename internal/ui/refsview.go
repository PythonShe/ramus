package ui

import (
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/signature"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// refsState is a tab's view of its branches, tags and remotes.
type refsState struct {
	kind int // refsBranches, refsTags, refsRemotes
	// sigs are the verdicts on tags' signatures asked for, by tag object.
	sigs   map[string]*sigResult
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
	// hasFolders says some row of rows is a folder, so that the names
	// are indented past the arrows.
	hasFolders bool
	rowsFor    rowsKey
	// rowsAsked is the key of rows being grouped off the main thread.
	rowsAsked rowsKey
	list      tableState
	chosen    int
	// byName indexes the branches and tags of byNameFor by full name.
	byName    map[string]int
	byNameFor *session.RefInfo
	// chosenName keeps the row chosen as rows come and go: a ref's full
	// name, or a folder's key in closed.
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
	open bool
	base string
	// stop cancels the search running.
	stop    func()
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
	if rs.kind == refsBranches {
		a.loadPulls(t)
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).MinHeight(barHeight).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.Segmented(c, &rs.kind, locales.RefsBranches(), locales.RefsTags(), locales.RefsRemotes())
			if rs.kind != refsRemotes {
				ui.SearchField(c, &rs.filter).Placeholder(locales.CommonFilter()).Label(locales.CommonFilter()).Width(fieldMedium).Bind(&a.find.refs)
			}
			ui.Spacer(c)
			a.remoteMenus(c, t)
			ro := t.s.Queue == nil
			switch rs.kind {
			case refsBranches:
				if d := rs.deleted; len(d) > 0 {
					what := locales.RefsDeletedBranches(len(d))
					if len(d) == 1 {
						what = locales.RefsDeletedBranch(redact.Control(strings.TrimPrefix(d[0].Name, "refs/heads/")), d[0].Commit[:min(10, len(d[0].Commit))])
					}
					ui.Text(c, what).FontSize(textSmall).TextColor(th.TextMuted).SingleLine()
					if ui.Button(c, locales.RefsRestore()).Disabled(ro).Clicked() {
						a.restoreBranches(t, d)
					}
				}
				if ui.Button(c, locales.RefsCleanUp()).Disabled(rs.info == nil).Clicked() {
					a.openCleanup(t)
				}
				if ui.Button(c, locales.KeysNewBranch()).Disabled(ro || len(t.s.State.Head.OID) == 0).Clicked() {
					a.branchUI = branchDialog{open: true, t: t, commit: hex.EncodeToString(t.s.State.Head.OID), checkout: true}
				}
			case refsTags:
				if ui.Button(c, locales.RefsNewTag()).Disabled(ro || len(t.s.State.Head.OID) == 0).Clicked() {
					a.tagDialog(t, "HEAD")
				}
			case refsRemotes:
				if ui.Button(c, locales.RefsAddRemote()).Disabled(ro).Clicked() {
					rs.dlg = refDialog{open: true, title: locales.RefsAddRemoteTitle(), labels: []string{locales.RefsName(), locales.CommonURL()}, values: []string{"", ""}, button: locales.RefsAdd(),
						run: func(d *refDialog) (write.Op, error) {
							return write.AddRemote(t.s.Where(), strings.TrimSpace(d.values[0]), strings.TrimSpace(d.values[1]))
						}}
				}
			}
		})
		switch {
		case rs.info == nil:
			loading(c, "")
			return
		case rs.info.Err != nil:
			errorMessage(c, errText(rs.info.Err))
			return
		}
		switch rs.kind {
		case refsBranches, refsTags:
			a.refList(c, t)
		case refsRemotes:
			a.remoteList(c, t)
		}
	})
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
		a.goWork(t, func() {
			rows := groupRefs(refNames(key.info, key.kind), key.filter, closed)
			update(func() {
				if rs.rowsAsked != key {
					return
				}
				rs.rows, rs.rowsFor = rows, key
				rs.hasFolders = slices.ContainsFunc(rows, func(r refRow) bool { return r.idx < 0 })
				rs.chosen = -1
				for i, r := range rows {
					name := closedPrefix(key.kind) + r.folder
					if r.idx >= 0 {
						name = fullRef(key.info, key.kind, r.idx)
					}
					if name == rs.chosenName {
						rs.chosen = i
						if rs.reveal {
							rs.list.ScrollTo(i, ui.Center)
						}
					}
				}
				rs.reveal = false
			})
		})
	}
	if rs.rowsFor.info == nil || rs.rowsFor.kind != rs.kind {
		loading(c, "")
		return
	}
	// The rows index the ref details they were grouped from, which stay
	// shown until the rows of newer ones come.
	info = rs.rowsFor.info
	rows := rs.rows
	if len(rows) == 0 {
		ui.Column(c).Fill().Center().Children(func() {
			what := locales.RefsNoBranches()
			switch {
			case rs.kind == refsTags && rs.filter != "":
				what = locales.RefsNoTagsMatch()
			case rs.kind == refsTags:
				what = locales.RefsNoTags()
			case rs.filter != "":
				what = locales.RefsNoBranchesMatch()
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
			return locales.RefsFolderLabel(openState(!rs.closed[prefix+rows[i].folder] || rs.filter != ""), redact.Control(rows[i].folder))
		}
		return redact.Control(rows[i].label)
	}
	var act func()
	rs.list.CellMenu = func(m *ui.Menu, i, _ int) {
		switch r := rows[i]; {
		case r.idx < 0:
		case rs.kind == refsBranches:
			a.branchMenu(m, t, info.Branches[r.idx], &act)
		default:
			a.tagMenu(m, t, info.Tags[r.idx], &act)
		}
	}
	table, cols := "branches", []ui.TableColumn{
		{Title: locales.ColumnName(), ID: "name", Width: nameColumn, MinWidth: 120},
		{Title: locales.ColumnUpstream(), ID: "upstream", Width: 190, MinWidth: 60},
		{Title: locales.ColumnLastCommit(), ID: "subject", MinWidth: 80},
		// Wide enough for the Switch button, and for "57 minutes ago".
		dateColumn(),
	}
	if rs.kind == refsTags {
		table, cols = "tags", []ui.TableColumn{
			cols[0],
			{Title: locales.ColumnKind(), ID: "kind", Width: 100, MinWidth: 60},
			{Title: locales.ColumnMessage(), ID: "subject", MinWidth: 80},
			dateColumn(),
		}
	}
	tbl := a.savedTable(c, table, &rs.list, cols, len(rows), func(i, col int) {
		r := rows[i]
		k := rs.list.ink(c, i)
		if r.idx < 0 {
			if col != 0 {
				return
			}
			ui.Row(c).Grow(1).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Width(float32(r.depth) * treeIndent)
				fk := prefix + r.folder
				if ui.Icon(c, disclosure(!rs.closed[fk] || rs.filter != "")).FontSize(iconSmall).TextColor(k.Muted).Width(iconMedium).Label(locales.RefsFolderToggle(openState(!rs.closed[fk] || rs.filter != ""))).RowClicked() {
					act = func() { rs.closed[fk] = !rs.closed[fk]; rs.closedGen++ }
				}
				ui.Text(c, redact.Control(r.label)+"/").SingleLine().Grow(1)
			})
			return
		}
		if col == 0 {
			// Indented as deep as the ref is, in a column as wide at
			// every depth, so the columns after it line up.
			var b session.BranchInfo
			full := ""
			if rs.kind == refsBranches {
				b = info.Branches[r.idx]
				full = strings.TrimPrefix(b.Name, "refs/heads/")
			} else {
				full = strings.TrimPrefix(info.Tags[r.idx].Name, "refs/tags/")
			}
			ui.Row(c).Grow(1).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				indent := float32(r.depth) * treeIndent
				if rs.hasFolders {
					// Room for the arrow, which the folders' rows have.
					indent += iconMedium + spaceM
				}
				ui.Box(c).Width(indent)
				name := ui.Text(c, redact.Control(r.label)).SingleLine().TruncateMiddle().Grow(1).Tooltip(redact.Control(full))
				if rs.kind == refsBranches && !head.Detached && b.Name == head.Target {
					name.Bold()
				}
				if b.Worktree != "" {
					ui.Text(c, locales.RefsInWorktree(filepath.Base(b.Worktree))).FontSize(textSmall).TextColor(k.Muted).SingleLine().Description(locales.RefsCheckedOutIn(b.Worktree))
				}
				if on := stackedOn(info, b.Name); on != "" {
					ui.Text(c, locales.RefsOnBranch(redact.Control(on))).FontSize(textSmall).TextColor(k.Muted).SingleLine().Description(locales.RefsStackedOn(redact.Control(on)))
				}
				if p, ok := t.pulls.by[b.Name]; ok && a.Settings.PullRequests {
					pullBadge(c, k, p)
				}
				if rs.kind == refsBranches {
					a.stackBadge(c, k, t, info, b)
				}
			})
			return
		}
		if rs.kind == refsBranches {
			b := info.Branches[r.idx]
			switch col {
			case 1:
				ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
					tr := ui.Text(c, tracking(b)).FontSize(textSmall).SingleLine().TruncateMiddle().Shrink(1)
					if b.Gone {
						tr.TextColor(k.Warning)
					} else {
						tr.TextColor(k.Muted)
					}
					// The counts apart from the name.
					if !b.Gone {
						aheadBehind(c, k, b.Ahead, b.Behind, strings.TrimPrefix(b.Upstream, "refs/remotes/"))
					}
				})
			case 2:
				ui.Text(c, redact.Control(b.Subject)).FontSize(textSmall).TextColor(k.Muted).SingleLine()
			case 3:
				// The branch chosen offers to switch to it where its time
				// is, so that the row keeps its width and its height;
				// hovering changes nothing, as in the other lists. In the
				// small text of the column it is lower than the row's name in
				// any font, as a font a little taller grows the row. One
				// checked out in another worktree cannot be switched to.
				// Beside it, the button of the row's menu.
				current := !head.Detached && b.Name == head.Target
				if rs.chosen != i {
					ui.Text(c, ago(now, time.Unix(b.Time, 0))).FontSize(textSmall).TextColor(k.Muted).SingleLine()
					return
				}
				ui.Row(c).Grow(1).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
					if !current && b.Worktree == "" && t.s.Queue != nil {
						if ui.Button(c, locales.RefsSwitch()).Label(locales.RefsSwitchTo(redact.Control(r.label))).FontSize(textSmall).Padding(0, spaceM).Clicked() {
							act = func() { a.checkOut(t, b) }
						}
						ui.Spacer(c)
					} else {
						ui.Text(c, ago(now, time.Unix(b.Time, 0))).FontSize(textSmall).TextColor(k.Muted).SingleLine().Shrink(1).Grow(1)
					}
					rowMenuButton(c, k, locales.RefsActionsFor(redact.Control(r.label)), func(m *ui.Menu) { a.branchMenu(m, t, b, &act) })
				})
			}
			return
		}
		tg := info.Tags[r.idx]
		switch col {
		case 1:
			kind := locales.RefsTagKindLightweight()
			switch {
			case tg.Signed:
				kind = locales.RefsTagKindSigned()
			case tg.Annotated:
				kind = locales.RefsTagKindAnnotated()
			}
			ui.Text(c, kind).FontSize(textSmall).TextColor(k.Muted).SingleLine()
		case 2:
			if v, ok := rs.sigs[tg.Object]; ok {
				sigText(c, k, v)
			} else {
				ui.Text(c, redact.Control(tg.Subject)).FontSize(textSmall).TextColor(k.Muted).SingleLine()
			}
		case 3:
			if rs.chosen != i {
				ui.Text(c, ago(now, time.Unix(tg.Time, 0))).FontSize(textSmall).TextColor(k.Muted).SingleLine()
				return
			}
			ui.Row(c).Grow(1).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
				ui.Text(c, ago(now, time.Unix(tg.Time, 0))).FontSize(textSmall).TextColor(k.Muted).SingleLine().Shrink(1).Grow(1)
				rowMenuButton(c, k, locales.RefsActionsFor(redact.Control(r.label)), func(m *ui.Menu) { a.tagMenu(m, t, tg, &act) })
			})
		}
	}).Label(locales.RefsListLabel()).Grow(1)
	// Right and Left open and close a folder, as a tree's.
	if rs.chosen >= 0 && rs.chosen < len(rows) && rows[rs.chosen].idx < 0 && rs.filter == "" {
		k := prefix + rows[rs.chosen].folder
		if shut := rs.closed[k]; shut && tbl.Shortcut(0, ui.KeyRight) || !shut && tbl.Shortcut(0, ui.KeyLeft) {
			act = func() { rs.closed[k] = !shut; rs.closedGen++ }
		}
	}
	// A double click or Enter checks a branch out, as its menu's first
	// item does, and opens or closes a folder.
	if tbl.Submitted() && rs.chosen >= 0 && rs.chosen < len(rows) {
		switch r := rows[rs.chosen]; {
		case r.idx < 0 && rs.filter == "":
			k := prefix + r.folder
			act = func() { rs.closed[k] = !rs.closed[k]; rs.closedGen++ }
		case r.idx >= 0 && rs.kind == refsBranches:
			b := info.Branches[r.idx]
			act = func() { a.checkOut(t, b) }
		}
	}
	if rs.chosen >= 0 && rs.chosen < len(rows) {
		if r := rows[rs.chosen]; r.idx >= 0 {
			rs.chosenName = fullRef(info, rs.kind, r.idx)
		} else {
			// A folder opened or closed stays chosen, for the keys.
			rs.chosenName = prefix + r.folder
		}
	}
	if act != nil {
		act()
	}
}

// rowMenuButton is the … at the end of a row that opens the menu its
// right-click does, named label, in the row's ink k: a menu found only by
// a right-click is one many never find.
func rowMenuButton(c *ui.Context, k ink, label string, build func(m *ui.Menu)) {
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Radius(radiusSmall).Padding(0, spaceXS).Shrink(0)
	if b.Hovered() {
		// A wash of the ink shows on the row's colour, chosen or not.
		b.Background(k.Muted.Alpha(0.15))
	}
	b.Children(func() { ui.Icon(c, glyphs().more).FontSize(iconMedium).TextColor(k.Muted) })
	b.Menu(build)
}

// verifyTag asks git about a tag's signature, shown in its row in place
// of its subject. It is kept by the tag object, so a tag made again is
// asked about again.
func (a *App) verifyTag(t *tab, tg session.TagInfo) {
	rs := &t.refs
	if rs.sigs == nil {
		rs.sigs = map[string]*sigResult{}
	}
	obj := tg.Object
	rs.sigs[obj] = nil
	t.s.Verify("", tg.Name, func(st signature.Status, err error) {
		if _, ok := rs.sigs[obj]; ok {
			rs.sigs[obj] = &sigResult{st: st, err: err}
		}
	})
}

// nameColumn is the width of the branch and tag name column.
const nameColumn = 340

// sidebarRefMenu gives a branch or tag in the sidebar the menu it has in
// the branches view, once the refs are read.
func (a *App) sidebarRefMenu(it ui.Element, t *tab, name string) {
	if strings.HasPrefix(name, "refs/remotes/") {
		it.ContextMenu(func(m *ui.Menu) {
			var act func()
			a.remoteBranchMenu(m, t, name, &act)
			if act != nil {
				act()
			}
		})
		return
	}
	if !strings.HasPrefix(name, "refs/heads/") && !strings.HasPrefix(name, "refs/tags/") {
		return
	}
	rs := &t.refs
	if !a.refIndex(t) {
		return
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

// remoteBranchMenu is what can be done with a remote branch, name a full
// ref name: as much as a local branch allows, and deleting it where it
// lives.
func (a *App) remoteBranchMenu(m *ui.Menu, t *tab, name string, act *func()) {
	ro := t.s.Queue == nil
	head := t.s.State.Head
	short := strings.TrimPrefix(name, "refs/remotes/")
	// What is deleted on the remote is what its fetch refspec maps there
	// from, not what the name looks like; the item says the remote the
	// name starts with when none does.
	remote, branch, mapped := "", "", false
	if r := t.s.Repo; r != nil {
		remote, branch, mapped = write.RemoteBranchOf(r.Config(), name)
	}
	if !mapped {
		remote, _, _ = strings.Cut(short, "/")
	}
	var tip []byte
	if l := t.s.State.Refs; l != nil {
		if i, ok := l.Find(name); ok {
			tip = l.OID(i)
		}
	}
	commit := hex.EncodeToString(tip)
	isHead := strings.HasSuffix(name, "/HEAD")
	if m.Item(locales.RefsSwitch()).Disabled(ro || isHead).Chosen() {
		*act = func() { a.checkOutRemote(t, name) }
	}
	if m.Item(locales.RefsNewWorktree()).Disabled(ro).Chosen() {
		*act = func() { a.openAddWorktree(t, name) }
	}
	into := "HEAD"
	if !head.Detached {
		into = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	if m.Item(locales.RefsMergeInto(redact.Control(into))).Disabled(ro || isHead).Chosen() {
		*act = func() { a.openMerge(t, name) }
	}
	if m.Item(locales.RefsCompareWith(redact.Control(into))).Disabled(len(head.OID) == 0 || len(tip) == 0).Chosen() {
		*act = func() { a.compareBranches(t, head.OID, tip, into, short, 0) }
	}
	m.Separator()
	if m.Item(locales.CommitCreateBranchHere()).Disabled(ro || len(tip) == 0).Chosen() {
		*act = func() { a.branchUI = branchDialog{open: true, t: t, commit: commit, from: name, checkout: true} }
	}
	if m.Item(locales.RefsCreateTagHere()).Disabled(ro || len(tip) == 0).Chosen() {
		*act = func() { a.tagDialog(t, commit) }
	}
	m.Separator()
	if m.Item(locales.RefsDeleteOnRemote(redact.Control(remote))).Disabled(ro || isHead || !mapped || len(tip) == 0).Danger().Chosen() {
		*act = func() {
			a.confirm = confirmState{open: true, title: locales.RefsDeleteRemoteBranchTitle(redact.Control(branch), redact.Control(remote)),
				message: locales.RefsDeleteRemoteBranchMessage(), button: locales.CommonDelete(),
				run: func() {
					op, err := write.DeleteRemoteBranch(t.s.Where(), remote, branch, commit)
					a.submit(t, op, err)
				}}
		}
	}
	if m.Item(locales.RefsCopyName()).Chosen() {
		a.clip = short
	}
	a.historyRefItems(m, t, name, act)
}

// refIndex reads the refs when they are not and indexes the branches and
// tags by full name in refsState.byName; it reports whether they are
// read.
func (a *App) refIndex(t *tab) bool {
	a.loadRefs(t)
	rs := &t.refs
	if rs.info == nil {
		return false
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
	return true
}

// sidebarSwitch switches to the branch a double click chose in the
// sidebar, as a double click does in the branches view. In a repository
// opened read-only it does nothing, as its menu items are off.
func (a *App) sidebarSwitch(t *tab, name string) {
	if t.s.Queue == nil {
		return
	}
	switch {
	case strings.HasPrefix(name, "refs/remotes/"):
		if !strings.HasSuffix(name, "/HEAD") {
			a.checkOutRemote(t, name)
		}
	case strings.HasPrefix(name, "refs/heads/"):
		a.switchTo(t, name)
	}
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
	if b.Gone {
		return locales.RefsUpstreamGone(up)
	}
	return up
}

// branchMenu is what can be done with a local branch.
func (a *App) branchMenu(m *ui.Menu, t *tab, b session.BranchInfo, act *func()) {
	ro := t.s.Queue == nil
	head := t.s.State.Head
	current := !head.Detached && head.Target == b.Name
	short := strings.TrimPrefix(b.Name, "refs/heads/")
	w, seen := t.s.Where(), t.s.Seen()
	if m.Item(locales.RefsSwitch()).Disabled(ro || current).Chosen() {
		*act = func() { a.checkOut(t, b) }
	}
	if m.Item(locales.RefsNewWorktree()).Disabled(ro || current || b.Worktree != "").Chosen() {
		*act = func() { a.openAddWorktree(t, b.Name) }
	}
	into := "HEAD"
	if !head.Detached {
		into = strings.TrimPrefix(head.Target, "refs/heads/")
	}
	if m.Item(locales.RefsMergeInto(redact.Control(into))).Disabled(ro || current).Chosen() {
		*act = func() { a.openMerge(t, b.Name) }
	}
	if m.Item(locales.RefsRebaseOnto(redact.Control(into))).Disabled(ro || current || head.Detached).Chosen() {
		*act = func() {
			op, err := write.Rebase(w, b.Name, t.s.LocalBranches(), seen)
			a.submitRewrite(t, op, err)
		}
	}
	if m.Item(locales.RefsCompareWith(redact.Control(into))).Disabled(current || len(head.OID) == 0).Chosen() {
		*act = func() {
			if id, err := hex.DecodeString(b.Commit); err == nil {
				a.compareBranches(t, head.OID, id, into, short, 0)
			}
		}
	}
	if info := t.refs.info; info != nil {
		if st := info.StackOf(b.Name); len(st) > 1 {
			if m.Item(locales.RefsPushStack(len(st))).Disabled(ro).Chosen() {
				*act = func() {
					if _, err := t.s.PushStack(st); err != nil {
						a.notice = errText(err)
					}
				}
			}
		}
	}
	m.Separator()
	if m.Item(locales.CommitCreateBranchHere()).Disabled(ro).Chosen() {
		*act = func() { a.branchUI = branchDialog{open: true, t: t, commit: b.Commit, from: b.Name, checkout: true} }
	}
	if m.Item(locales.RefsCreateTagHere()).Disabled(ro).Chosen() {
		*act = func() { a.tagDialog(t, b.Commit) }
	}
	if m.Item(locales.RefsRenameItem()).Disabled(ro).Chosen() {
		*act = func() {
			t.refs.dlg = refDialog{open: true, title: locales.RefsRenameTitle(redact.Control(short)), labels: []string{locales.RefsNewName()}, values: []string{short}, button: locales.CommonRename(),
				run: func(d *refDialog) (write.Op, error) {
					return write.RenameBranch(w, b.Name, strings.TrimSpace(d.values[0]), seen)
				}}
		}
	}
	if m.Item(locales.RefsDeleteItem()).Disabled(ro || current).Danger().Chosen() {
		*act = func() { a.deleteBranches(t, []session.BranchInfo{b}) }
	}
	m.Separator()
	if m.Item(locales.RefsCopyName()).Chosen() {
		a.clip = short
	}
	a.pullItems(m, t, b, act)
	m.Separator()
	pin := locales.RefsPinToTop()
	if slices.Contains(t.prefs.pinned, b.Name) {
		pin = locales.RefsUnpin()
	}
	if m.Item(pin).Disabled(!t.prefs.loaded).Chosen() {
		*act = func() { a.toggleRef(t, &t.prefs.pinned, b.Name) }
	}
	a.historyRefItems(m, t, b.Name, act)
}

// historyRefItems hide a ref from history, show it again, or show it
// alone.
func (a *App) historyRefItems(m *ui.Menu, t *tab, name string, act *func()) {
	hide := locales.RefsHideInHistory()
	if slices.Contains(t.prefs.hidden, name) {
		hide = locales.RefsShowInHistory()
	}
	if m.Item(hide).Disabled(!t.prefs.loaded).Chosen() {
		*act = func() { a.toggleRef(t, &t.prefs.hidden, name) }
	}
	if m.Item(locales.RefsShowOnlyInHistory()).Chosen() {
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
	if t.s.Queue == nil {
		return
	}
	if b.Worktree != "" {
		a.elsewhereUI = elsewhere{open: true, branch: b.Name, folder: b.Worktree}
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
	choice := ui.AlertDialog(c, &open, locales.RefsSwitchTitle(name), locales.RefsSwitchMessage(),
		locales.RefsCarryThem(), locales.RefsStashThem(), locales.CommonCancel())
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
	title := locales.RefsDeleteBranchesTitle(len(bs))
	if len(bs) == 1 {
		b := bs[0]
		title = locales.RefsDeleteBranchTitle(redact.Control(strings.TrimPrefix(b.Name, "refs/heads/")), b.Commit[:min(10, len(b.Commit))])
	}
	msg := locales.RefsDeleteBranchesMessage()
	var wts []string
	for _, b := range bs {
		if b.Worktree != "" {
			wts = append(wts, b.Worktree)
		}
	}
	if len(wts) > 0 {
		msg = locales.RefsDeleteBranchesWorktreesMessage(strings.Join(wts, ", "))
	}
	w, seen := t.s.Where(), t.s.Seen()
	t.refs.ask = confirmState{open: true, title: title, message: msg, button: locales.CommonDelete(), run: func() {
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
		op.Name = locales.RefsRestoreBranchJob(strings.TrimPrefix(b.Name, "refs/heads/"), b.Commit[:min(12, len(b.Commit))])
		a.submit(t, op, err)
	}
	t.refs.deleted = nil
}

func (a *App) tagMenu(m *ui.Menu, t *tab, tg session.TagInfo, act *func()) {
	ro := t.s.Queue == nil
	short := strings.TrimPrefix(tg.Name, "refs/tags/")
	if m.Item(locales.RefsCheckOut()).Disabled(ro || tg.Commit == "").Chosen() {
		*act = func() { a.submit(t, write.Detach(t.s.Where(), tg.Commit, t.s.Seen()), nil) }
	}
	var remotes []session.RemoteInfo
	if t.refs.info != nil {
		remotes = t.refs.info.Remotes
	}
	m.Submenu(locales.RefsPushTo(), func(sub *ui.Menu) {
		for _, r := range remotes {
			if sub.Item(redact.Control(r.Name)).Disabled(ro).Chosen() {
				*act = func() {
					op, err := write.PushTag(t.s.Where(), r.Name, short)
					a.submit(t, op, err)
				}
			}
		}
	}).Disabled(len(remotes) == 0)
	if m.Item(locales.RefsDeleteItem()).Disabled(ro).Danger().Chosen() {
		*act = func() {
			t.refs.ask = confirmState{open: true, title: locales.RefsDeleteTagTitle(redact.Control(short), tg.Object[:min(10, len(tg.Object))]),
				message: locales.RefsDeleteTagMessage(), button: locales.CommonDelete(),
				run: func() { a.submit(t, write.DeleteTag(t.s.Where(), short, tg.Object), nil) }}
		}
	}
	if tg.Signed && m.Item(locales.RefsCheckSignature()).Chosen() {
		*act = func() { a.verifyTag(t, tg) }
	}
	m.Separator()
	if m.Item(locales.RefsCopyName()).Chosen() {
		a.clip = short
	}
}

// tagDialog asks for a tag to make at commit.
func (a *App) tagDialog(t *tab, commit string) {
	shown := commit
	if len(shown) > 10 {
		shown = shown[:10]
	}
	t.refs.dlg = refDialog{open: true, title: locales.RefsCreateTagTitle(shown), labels: []string{locales.RefsTagName()}, values: []string{""}, tag: true, kind: 1, button: locales.CommonCreate(),
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
	a.modal(c, &d.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
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
				ui.Segmented(c, &d.kind, locales.RefsTagLightweight(), locales.RefsTagAnnotated(), locales.RefsTagSigned())
				if d.kind > 0 {
					ui.TextArea(c, &d.message).Placeholder(locales.CommonMessage()).Label(locales.CommonMessage()).Height(textAreaH)
				}
			}
			if d.err != "" {
				ui.Text(c, d.err).FontSize(textSmall).TextColor(th.Danger).Selectable()
			}
			empty := false
			for _, v := range d.values {
				empty = empty || strings.TrimSpace(v) == ""
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
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
	glyphMenuButton(c, glyphs().refresh, locales.RefsFetch(), func(m *ui.Menu) {
		if m.Item(locales.RefsFetchAll()).Disabled(none).Chosen() {
			op, err := write.Fetch(w, "", rs.prune)
			a.submit(t, op, err)
		}
		for _, r := range remotes {
			if m.Item(locales.RefsFetchRemote(redact.Control(r.Name))).Disabled(none).Chosen() {
				op, err := write.Fetch(w, r.Name, rs.prune)
				a.submit(t, op, err)
			}
		}
		m.Separator()
		if m.Item(locales.RefsPruneDeleted()).Checked(rs.prune).Chosen() {
			rs.prune = !rs.prune
		}
	})
	noUp := none || cur == nil || cur.Upstream == "" || cur.Gone
	glyphMenuButton(c, glyphs().arrowDown, locales.RefsPull(), func(m *ui.Menu) {
		for _, mode := range []struct {
			label string
			mode  write.PullMode
		}{
			{locales.RefsPullMerging(), write.PullMerge},
			{locales.RefsPullRebasing(), write.PullRebase},
			{locales.RefsPullFFOnly(), write.PullFFOnly},
		} {
			if m.Item(mode.label).Disabled(noUp).Chosen() {
				op, err := write.Pull(w, mode.mode, t.s.LocalBranches(), t.s.Seen())
				a.submit(t, op, err)
			}
		}
	})
	glyphMenuButton(c, glyphs().arrowUp, locales.RefsPush(), func(m *ui.Menu) {
		// The branch the menu is for: a push refuses once HEAD is on
		// another, which git would push in its place.
		branch := t.s.Seen().Branch
		if m.Item(locales.RefsPush()).Disabled(noUp).Chosen() {
			a.submit(t, write.Push(w, write.PushOptions{Branch: branch}), nil)
		}
		for _, r := range remotes {
			if m.Item(locales.RefsPushAndTrack(redact.Control(r.Name))).Disabled(none || cur == nil).Chosen() {
				a.submit(t, write.Push(w, write.PushOptions{Remote: r.Name, Branch: branch}), nil)
			}
		}
		m.Separator()
		if m.Item(locales.RefsForcePushItem()).Disabled(noUp).Danger().Chosen() {
			up := strings.TrimPrefix(cur.Upstream, "refs/remotes/")
			rs.ask = confirmState{open: true, title: locales.RefsForcePushTitle(redact.Control(up)),
				message: locales.RefsForcePushMessage(), button: locales.RefsForcePush(),
				run: func() { a.submit(t, write.Push(w, write.PushOptions{ForceWithLease: true, Branch: branch}), nil) }}
		}
	})
}

func (a *App) remoteList(c *ui.Context, t *tab) {
	th := c.Theme()
	rs := &t.refs
	remotes := rs.info.Remotes
	if len(remotes) == 0 {
		emptyState(c, locales.RefsNoRemotes())
		return
	}
	ro := t.s.Queue == nil
	w := t.s.Where()
	var act func()
	ui.Column(c).Grow(1).Children(func() {
		for _, r := range remotes {
			row := ui.Row(c.Key(r.Name)).Padding(barPadY, insetX).Gap(spaceL).AlignItems(ui.Center).FillWidth()
			menu := func(m *ui.Menu) {
				if m.Item(locales.RefsFetch()).Disabled(ro).Chosen() {
					act = func() {
						op, err := write.Fetch(w, r.Name, rs.prune)
						a.submit(t, op, err)
					}
				}
				if m.Item(locales.RefsRenameItem()).Disabled(ro).Chosen() {
					act = func() {
						rs.dlg = refDialog{open: true, title: locales.RefsRenameRemoteTitle(redact.Control(r.Name)), labels: []string{locales.RefsNewName()}, values: []string{r.Name}, button: locales.CommonRename(),
							run: func(d *refDialog) (write.Op, error) {
								return write.RenameRemote(w, r.Name, strings.TrimSpace(d.values[0]))
							}}
					}
				}
				if m.Item(locales.RefsChangeURLItem()).Disabled(ro).Chosen() {
					act = func() {
						// A URL with a credential in it is not put back in
						// the field, where it would show.
						url := r.URL
						if redact.Secrets(url) != url {
							url = ""
						}
						rs.dlg = refDialog{open: true, title: locales.RefsChangeURLTitle(redact.Control(r.Name)), labels: []string{locales.CommonURL()}, values: []string{url}, button: locales.RefsChange(),
							run: func(d *refDialog) (write.Op, error) {
								return write.SetRemoteURL(w, r.Name, strings.TrimSpace(d.values[0]))
							}}
					}
				}
				m.Separator()
				if m.Item(locales.RefsRemoveItem()).Disabled(ro).Danger().Chosen() {
					act = func() {
						rs.ask = confirmState{open: true, title: locales.RefsRemoveRemoteTitle(redact.Control(r.Name)),
							message: locales.RefsRemoveRemoteMessage(), button: locales.CommonRemove(),
							run: func() {
								op, err := write.RemoveRemote(w, r.Name)
								a.submit(t, op, err)
							}}
					}
				}
			}
			row.Children(func() {
				ui.Text(c, redact.Control(r.Name)).Bold().Width(160).SingleLine()
				ui.Column(c).Grow(1).Children(func() {
					ui.Text(c, redact.Shown(r.URL)).FontSize(textSmall).SingleLine().Selectable()
					if r.PushURL != "" && r.PushURL != r.URL {
						ui.Text(c, locales.RefsPushURL(redact.Shown(r.PushURL))).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Selectable()
					}
				})
				// The menu by a button too: the rows are few, and none is
				// chosen to show it on.
				rowMenuButton(c, plainInk(th), locales.RefsActionsFor(redact.Control(r.Name)), menu)
			})
			row.ContextMenu(menu)
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
	cs.stop = t.s.FindCleanup(base, current, info.Branches, func(found []session.Candidate, err error) {
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
		{session.Merged, locales.RefsWhyMerged()},
		{session.RebaseMerged, locales.RefsWhyRebased()},
		{session.SquashMerged, locales.RefsWhySquashed()},
		{session.UpstreamGone, locales.RefsWhyUpstreamGone()},
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
		if cs.stop != nil {
			// A search for a sheet closed runs git for each branch to no
			// end.
			cs.stop()
			cs.stop = nil
		}
		return
	}
	th := c.Theme()
	a.modal(c, &cs.open, func() {
		dialogBox(c).Width(dialogWide).Height(480).Children(func() {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.RefsCleanupTitle()).Bold().Grow(1)
				ui.Text(c, locales.RefsCleanupAgainst()).TextColor(th.TextMuted)
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
				loading(c, "")
			case cs.err != "":
				ui.Text(c, cs.err).TextColor(th.Danger).Selectable().Grow(1)
			case len(cs.found) == 0:
				ui.Column(c).Grow(1).Center().Children(func() {
					ui.Text(c, locales.RefsCleanupNone()).TextColor(th.TextMuted)
				})
			default:
				found := cs.found
				cs.list.Label = func(i int) string { return strings.TrimPrefix(found[i].Branch.Name, "refs/heads/") }
				ui.List(c, &cs.list, len(found), func(i int) {
					f := found[i]
					ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
						ui.Checkbox(c, &cs.picked[i], redact.Control(strings.TrimPrefix(f.Branch.Name, "refs/heads/"))).Grow(1)
						if f.Branch.Worktree != "" {
							ui.Text(c, locales.RefsInWorktree(filepath.Base(f.Branch.Worktree))).FontSize(textSmall).TextColor(th.TextMuted).SingleLine()
						}
						ui.Text(c, whyWords(f.Why)).FontSize(textSmall).TextColor(th.TextMuted).Width(200).SingleLine()
					})
				}).Label(locales.RefsCleanupListLabel()).Grow(1)
			}
			n := 0
			for _, p := range cs.picked {
				if p {
					n++
				}
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					cs.open = false
				}
				label := locales.CommonDelete()
				if n > 0 {
					label = locales.RefsCleanupDelete(n)
				}
				if dangerButton(c, label, n == 0 || t.s.Queue == nil || cs.loading).Clicked() {
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
