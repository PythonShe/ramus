package ui

import (
	"encoding/hex"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// changesState is a tab's changes list.
type changesState struct {
	model *changeModel
	// building is set while a model is made off the main thread; built*
	// are what the last one asked for.
	building    bool
	builtFor    *status.Status
	builtFilter string
	builtTree   bool
	builtOpen   int
	filter      string
	mode        int // modeList, modeTree or modeReview
	open        map[openKey]bool
	openGen     int
	list        ui.ListState
	chosen      int
	// want is the path of a file to choose once the list is built: one
	// chosen when the app last ran.
	want string
	// keep is the place of the file chosen when it was staged, unstaged
	// or resolved: once it has left its section, the file now in its
	// place there is chosen, so the next can be staged in turn.
	keep    keptPlace
	picked  ui.Selection[rowKey]
	confirm confirmState
	diff    diffState
	// lines are the lines each change adds and deletes, counted for the
	// status countFor asked; totals adds them up by section, for the
	// model and the count totalsFor names.
	lines     session.ChangeLines
	countFor  *status.Status
	totals    [sections]diff.Lines
	totalsFor struct {
		m  *changeModel
		st *status.Status
	}
}

// leaveCount forgets a count still running when its view is left, which
// stops it, so that it runs again when the view is shown.
func (cv *changesState) leaveCount() {
	if cv.countFor != cv.lines.For {
		cv.countFor = nil
	}
}

// countLines counts the lines of the changes of the status last read,
// once per status.
func (cv *changesState) countLines(t *tab) {
	st := t.s.State.Status
	if st == nil || st == cv.countFor {
		return
	}
	cv.countFor = st
	t.s.CountChanges(st, func(l session.ChangeLines) { cv.lines = l })
}

// entryLines is the count of entry i of st in sec: from the count of
// st, or, while that is counted, by path from the count before.
func (cv *changesState) entryLines(st *status.Status, i int, sec section) diff.Lines {
	l := &cv.lines
	if l.For == nil || sec == secConflicts {
		return diff.Lines{}
	}
	var staged, unstaged diff.Lines
	if l.For == st {
		staged, unstaged = l.Staged[i], l.Unstaged[i]
	} else {
		staged, unstaged = l.Find(st.Path(i))
	}
	if sec == secStaged {
		return staged
	}
	return unstaged
}

// sectionLines adds up the counts of sec's files in m.
func (cv *changesState) sectionLines(m *changeModel, sec section) diff.Lines {
	if cv.totalsFor.m != m || cv.totalsFor.st != cv.lines.For {
		cv.totalsFor.m, cv.totalsFor.st = m, cv.lines.For
		for s := range sections {
			cv.totals[s] = diff.Lines{}
			for _, i := range m.files[s] {
				cv.totals[s].Add(cv.entryLines(m.st, int(i), s))
			}
		}
	}
	return cv.totals[sec]
}

// confirmState is a question asked before a write that drops work.
type confirmState struct {
	open           bool
	title, message string
	button         string
	run            func()
}

func newChangesState() changesState {
	return changesState{chosen: -1, open: map[openKey]bool{}, diff: newDiffState()}
}

// rebuildChanges makes the model again, off the main thread, when the
// status, the filter, the mode or the folders open changed.
func (a *App) rebuildChanges(t *tab) {
	cv := &t.cv
	st := t.s.State.Status
	tree := cv.mode == modeTree
	if st == nil || cv.building || (cv.builtFor == st && cv.builtFilter == cv.filter && cv.builtTree == tree && cv.builtOpen == cv.openGen) {
		return
	}
	cv.building = true
	filter, gen := cv.filter, cv.openGen
	open := make(map[openKey]bool, len(cv.open))
	for k, v := range cv.open {
		open[k] = v
	}
	update := a.Update
	a.goWork(t, func() {
		m := buildChanges(st, filter, tree, open)
		update(func() {
			cv.building = false
			old := cv.model
			cv.model, cv.builtFor, cv.builtFilter, cv.builtTree, cv.builtOpen = m, st, filter, tree, gen
			cv.findWanted()
			cv.placeKept(old)
		})
	})
}

// keptPlace is where a file chosen was in its section, the nth file
// row there, as it left it.
type keptPlace struct {
	key rowKey
	at  int
	set bool
}

// keepPlace remembers the place of the row chosen in m when it is among
// rows, which a write is about to move out of their section.
func (cv *changesState) keepPlace(m *changeModel, rows []changeRow) {
	cv.keep = keptPlace{}
	if cv.chosen < 0 || cv.chosen >= len(m.rows) {
		return
	}
	r := m.rows[cv.chosen]
	if r.kind != rowFile || !slices.ContainsFunc(rows, func(o changeRow) bool { return m.key(o) == m.key(r) }) {
		return
	}
	at := 0
	for _, o := range m.rows[:cv.chosen] {
		if o.kind == rowFile && o.sec == r.sec {
			at++
		}
	}
	cv.keep = keptPlace{key: m.key(r), at: at, set: true}
}

// keepChosen remembers the place of the row chosen, which a write on it
// alone is about to move out of its section.
func (cv *changesState) keepChosen() {
	if m := cv.model; m != nil && cv.chosen >= 0 && cv.chosen < len(m.rows) {
		cv.keepPlace(m, m.rows[cv.chosen:cv.chosen+1])
	}
}

// placeKept chooses, in the model just built, the file now where the
// kept one was in its section, or, with the section empty, the kept
// file where it went. old is the model before, whose choice says
// whether the user chose something else meanwhile.
func (cv *changesState) placeKept(old *changeModel) {
	k := &cv.keep
	m := cv.model
	if !k.set || m == nil {
		return
	}
	if old == nil || cv.chosen < 0 || cv.chosen >= len(old.rows) || old.key(old.rows[cv.chosen]) != k.key {
		*k = keptPlace{}
		return
	}
	var in []int
	for i, r := range m.rows {
		if r.kind != rowFile || r.sec != k.key.sec {
			continue
		}
		if m.key(r) == k.key {
			return // not moved yet: the write is still on its way
		}
		in = append(in, i)
	}
	choose := -1
	if len(in) > 0 {
		choose = in[min(k.at, len(in)-1)]
	} else {
		for i, r := range m.rows {
			if r.kind == rowFile && string(m.st.Path(int(r.idx))) == k.key.path {
				choose = i
				break
			}
		}
	}
	*k = keptPlace{}
	if choose >= 0 {
		cv.chosen = choose
		cv.list.Choose(choose)
		cv.list.ScrollIntoView(choose)
	}
}

// findWanted chooses the wanted file in a model just built, if it is
// there.
func (cv *changesState) findWanted() {
	m := cv.model
	if cv.want == "" || m == nil {
		return
	}
	for i, r := range m.rows {
		if r.kind == rowFile && string(m.st.Path(int(r.idx))) == cv.want {
			cv.chosen = i
			cv.list.ScrollTo(i, ui.Center)
			break
		}
	}
	cv.want = ""
}

func (a *App) changesList(c *ui.Context, t *tab) {
	th := c.Theme()
	cv := &t.cv
	a.rebuildChanges(t)
	cv.countLines(t)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.SearchField(c, &cv.filter).Label(locales.ChangesFilterLabel()).Placeholder(locales.ChangesFilterPlaceholder()).Grow(1)
			changesModes(c, cv)
			ro := t.s.Queue == nil
			ui.PopoverMenuButton(c, locales.ChangesMore(), func(m *ui.Menu) {
				if m.Item(locales.ChangesStashChanges()).Disabled(ro).Chosen() {
					a.openStash(t)
				}
				if m.Item(locales.ChangesCleanUntracked()).Disabled(ro).Chosen() {
					a.openClean(t)
				}
				if m.Item(locales.ChangesApplyPatchFile()).Disabled(ro || a.PickFile == nil).Chosen() {
					a.applyPatchFile(t)
				}
			})
		})
		m := cv.model
		if m == nil {
			loading(c, "")
			return
		}
		if len(m.rows) == 0 {
			ui.Column(c).Fill().Center().Children(func() {
				msg := locales.ChangesNone()
				if m.filter != "" {
					msg = locales.ChangesNoneMatch(m.filter)
				}
				ui.Text(c, msg).TextColor(th.TextMuted)
			})
			return
		}
		rows := m.rows
		cv.list.Key = func(i int) any { return m.key(rows[i]) }
		cv.list.Selected = &cv.chosen
		cv.list.Selection = &cv.picked
		cv.list.Header = func(i int) bool { return rows[i].kind == rowHeader }
		cv.list.Label = func(i int) string { return rowLabel(cv, m, rows[i]) }
		var act func()
		l := ui.List(c, &cv.list, len(rows)).Rows(func(r ui.ListRow) { a.changeRow(c, t, m, rows[r.Index], rowInk(r), &act) }).Label(locales.ChangesListLabel()).Grow(1)
		if cv.chosen >= 0 && cv.chosen < len(rows) {
			r := rows[cv.chosen]
			// A folder opens and closes as a tree's does: by Right and
			// Left, and by Enter or a double click, which stage a file.
			// Its menu stages it.
			if r.kind == rowDir {
				key := openKey{r.sec, m.dir(r.sec, r.idx)}
				open := cv.open[key]
				if !open && l.Shortcut(0, ui.KeyRight) || open && l.Shortcut(0, ui.KeyLeft) || l.Submitted() {
					act = func() { cv.open[key] = !open; cv.openGen++ }
				}
			} else if l.Submitted() {
				act = func() { a.toggleStage(t, m, []changeRow{r}) }
			}
		}
		// Delete asks to discard what is chosen, as the menu's Discard
		// does, with its snapshot to undo it.
		if (cv.list.Shortcut(c, 0, ui.KeyDelete) || cv.list.Shortcut(c, 0, ui.KeyBackspace)) && cv.chosen >= 0 && cv.chosen < len(rows) && t.s.Queue != nil {
			if r := rows[cv.chosen]; r.kind != rowHeader && r.sec == secUnstaged {
				es := m.entries(cv.chosenRows(m, r))
				act = func() { a.askDiscard(t, m, es) }
			}
		}
		if act != nil {
			act()
		}
	})
	a.confirmView(c, &cv.confirm)
}

// changesPane is the changes list with the commit editor under it.
// The ways the changes view shows the changes: the files as a list or in
// their folders, beside the patch of the one chosen, or every patch not
// staged yet one after the other, to review (review.go).
const (
	modeList = iota
	modeTree
	modeReview
)

// changesModes chooses among them, at the top of each.
func changesModes(c *ui.Context, cv *changesState) {
	ui.Segmented(c, &cv.mode, locales.ChangesList(), locales.ChangesTree(), locales.ChangesReview())
}

func (a *App) changesPane(c *ui.Context, t *tab) {
	ui.Column(c).Fill().Children(func() {
		a.monitorOffer(c, t)
		ui.Column(c).Grow(1).Children(func() { a.changesList(c, t) })
		a.commitBox(c, t)
	})
}

func rowLabel(cv *changesState, m *changeModel, r changeRow) string {
	switch r.kind {
	case rowHeader:
		return locales.ChangesSectionLabel(r.sec.title(), len(m.files[r.sec]))
	case rowDir:
		return locales.ChangesFolderLabel(openState(cv.open[openKey{r.sec, m.dir(r.sec, r.idx)}]), m.name(r))
	}
	i := int(r.idx)
	return fileLabel(entryWords(m.st, i, r.sec), string(m.st.Path(i)), cv.entryLines(m.st, i, r.sec))
}

func (a *App) changeRow(c *ui.Context, t *tab, m *changeModel, r changeRow, k ink, act *func()) {
	th := c.Theme()
	cv := &t.cv
	ro := t.s.Queue == nil
	if r.kind == rowHeader {
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Background(th.Surface).Children(func() {
			ui.Text(c, r.sec.header(len(m.files[r.sec]))).Bold().Grow(1)
			tot := cv.sectionLines(m, r.sec)
			lineCounts(c, int(tot.Added), int(tot.Deleted))
			var label string
			switch r.sec {
			case secStaged:
				label = locales.ChangesUnstageAll()
			case secUnstaged:
				label = locales.ChangesStageAll()
			}
			if label != "" && ui.Button(c, label).Disabled(ro).Clicked() {
				sec := r.sec
				*act = func() { a.stageEntries(t, m, sec, m.files[sec]) }
			}
		})
		return
	}
	indent := float32(r.depth) * 16
	row := ui.Row(c).Padding(rowPadY, insetX).Gap(spaceS).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Box(c).Width(indent)
		if r.kind == rowDir {
			key := openKey{r.sec, m.dir(r.sec, r.idx)}
			arrow := "▸"
			if cv.open[key] {
				arrow = "▾"
			}
			if ui.Text(c, arrow).Width(12).TextColor(k.Muted).RowClicked() {
				*act = func() { cv.open[key] = !cv.open[key]; cv.openGen++ }
			}
			ui.Text(c, m.name(r)+"/").SingleLine().Grow(1)
			return
		}
		i := int(r.idx)
		letter := entryLetter(m.st, i, r.sec)
		ui.Text(c, letter).Font("monospace").TextColor(entryColor(k, m.st, i, letter)).Width(16).Description(entryWords(m.st, i, r.sec))
		if untrackedFolder(m.st, i) {
			ui.Icon(c, changeIcons().folder).FontSize(iconLarge).Width(iconLarge).TextColor(k.Muted)
		} else {
			fileIcon(c, string(m.st.Path(i)))
		}
		shown, flagged := showPath(m.name(r))
		if f, why := showPath(string(m.st.Path(i))); why != "" {
			shown, flagged = f, why
		}
		name := pathLabel(c, shown).Grow(1)
		if flagged != "" {
			name.TextColor(k.Warning).Description(flagged)
		}
		if note := entryNote(m.st, i, r.sec); note != "" {
			ui.Text(c, note).FontSize(textSmall).TextColor(k.Muted).SingleLine()
		}
		inkFileLines(c, k, cv.entryLines(m.st, i, r.sec))
	})
	if r.kind == rowFile && row.RowDoubleClicked() {
		*act = func() { a.toggleStage(t, m, []changeRow{r}) }
	}
	row.ContextMenu(func(menu *ui.Menu) { a.changeMenu(menu, t, m, r, act) })
}

// openState is how a folder's label names whether it is open.
func openState(open bool) string {
	if open {
		return "open"
	}
	return "closed"
}

// entryLetter is the status letter shown for entry i in sec: git's
// own, but U for a file or folder git does not track, as most editors
// show it, and ! for a conflict, which git writes U.
func entryLetter(st *status.Status, i int, sec section) string {
	switch st.Kind(i) {
	case status.Untracked:
		return "U"
	case status.Unmerged:
		return "!"
	}
	x, y := st.XY(i)
	if sec == secStaged {
		return string(x)
	}
	return string(y)
}

// entryWords names the state, so it is never told by a letter or a
// colour alone.
func entryWords(st *status.Status, i int, sec section) string {
	switch st.Kind(i) {
	case status.Untracked:
		if untrackedFolder(st, i) {
			return locales.ChangesState("untrackedFolder")
		}
		return locales.ChangesState("untracked")
	case status.Unmerged:
		return locales.ChangesState("conflicted")
	}
	// The messages choose their words by the status letter.
	if sec == secStaged {
		return locales.ChangesStagedState(entryLetter(st, i, sec))
	}
	return locales.ChangesState(entryLetter(st, i, sec))
}

// Git file modes with meanings of their own.
const (
	modeSymlink = 0o120000
	modeGitlink = 0o160000
	modeExec    = 0o100755
	modeFile    = 0o100644
)

// entryNote says what a change is when it is not plain file content: a
// symlink, a submodule, an executable bit, a rename.
func entryNote(st *status.Status, i int, sec section) string {
	switch st.Kind(i) {
	case status.Changed, status.Renamed:
	default:
		return ""
	}
	m := st.Modes(i)
	from, to := m[1], m[2] // index to work tree
	if sec == secStaged {
		from, to = m[0], m[1]
	}
	var notes []string
	if st.Kind(i) == status.Renamed && sec == secStaged {
		notes = append(notes, locales.ChangesNoteFrom(string(st.OrigPath(i))))
	}
	switch {
	case to == modeGitlink || from == modeGitlink:
		notes = append(notes, submoduleNote(st.Submodule(i)))
	case to == modeSymlink || from == modeSymlink:
		if from != to && from != 0 && to != 0 {
			notes = append(notes, locales.ChangesNoteNowA(modeName(to)))
		} else {
			notes = append(notes, locales.ChangesModeSymlink())
		}
	case from != to && from != 0 && to != 0:
		notes = append(notes, locales.ChangesNoteModeChange(modeName(from), modeName(to)))
	}
	return strings.Join(notes, ", ")
}

func modeName(m uint32) string {
	switch m {
	case modeSymlink:
		return locales.ChangesModeSymlink()
	case modeGitlink:
		return locales.ChangesModeSubmodule()
	case modeExec:
		return locales.ChangesModeExecutable()
	case modeFile:
		return locales.ChangesModeFile()
	}
	return locales.ChangesModeOther(strconv.FormatUint(uint64(m), 8))
}

// submoduleNote reads porcelain v2's S<c><m><u>.
func submoduleNote(s string) string {
	if len(s) != 4 || s[0] != 'S' {
		return locales.ChangesModeSubmodule()
	}
	var what []string
	if s[1] == 'C' {
		what = append(what, locales.ChangesNoteNewCommits())
	}
	if s[2] == 'M' {
		what = append(what, locales.ChangesNoteModifiedContent())
	}
	if s[3] == 'U' {
		what = append(what, locales.ChangesNoteUntrackedContent())
	}
	if len(what) == 0 {
		return locales.ChangesModeSubmodule()
	}
	return locales.ChangesNoteSubmodule(strings.Join(what, ", "))
}

// chosenRows are the rows an action from row r acts on: the rows picked
// when r is one of them, else r alone.
func (cv *changesState) chosenRows(m *changeModel, r changeRow) []changeRow {
	if cv.picked.Len() < 2 || !cv.picked.Has(m.key(r)) {
		return []changeRow{r}
	}
	var out []changeRow
	for _, row := range m.rows {
		if row.kind != rowHeader && row.sec == r.sec && cv.picked.Has(m.key(row)) {
			out = append(out, row)
		}
	}
	return out
}

// entries are the status entries rows cover, folders expanded.
func (m *changeModel) entries(rows []changeRow) []int32 {
	var out []int32
	seen := map[int32]bool{}
	for _, r := range rows {
		var es []int32
		switch r.kind {
		case rowFile:
			es = []int32{r.idx}
		case rowDir:
			es = m.under(r.sec, m.dir(r.sec, r.idx), true)
		}
		for _, e := range es {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	return out
}

// paths are the paths a write on entries names: both of a rename.
func paths(st *status.Status, entries []int32) []string {
	var out []string
	for _, e := range entries {
		out = append(out, string(st.Path(int(e))))
		if st.Kind(int(e)) == status.Renamed {
			out = append(out, string(st.OrigPath(int(e))))
		}
	}
	return out
}

func (a *App) changeMenu(menu *ui.Menu, t *tab, m *changeModel, r changeRow, act *func()) {
	rows := t.cv.chosenRows(m, r)
	es := m.entries(rows)
	ro := t.s.Queue == nil
	n := len(es)
	switch r.sec {
	case secStaged:
		if menu.Item(locales.ChangesUnstageFiles(n)).Disabled(ro || n == 0).Chosen() {
			*act = func() { a.stageEntries(t, m, r.sec, es) }
		}
	case secUnstaged:
		if menu.Item(locales.ChangesStageFiles(n)).Disabled(ro || n == 0).Chosen() {
			*act = func() { a.stageEntries(t, m, r.sec, es) }
		}
		if menu.Item(locales.ChangesDiscardFiles(n)).Disabled(ro || n == 0).Danger().Chosen() {
			*act = func() { a.askDiscard(t, m, es) }
		}
	case secConflicts:
		a.conflictMenu(menu, t, m, es, act)
		if menu.Item(locales.ChangesMarkResolved(n)).Disabled(ro || n == 0).Chosen() {
			*act = func() { a.stageEntries(t, m, secUnstaged, es) }
		}
	}
	if r.kind == rowFile && r.sec != secStaged {
		p := string(m.st.Path(int(r.idx)))
		menu.Separator()
		if menu.Item(locales.ChangesIgnorePath(p)).Disabled(ro).Chosen() {
			*act = func() { a.ignore(t, write.IgnorePattern(p)) }
		}
		if ext := path.Ext(p); ext != "" && !strings.ContainsAny(ext, `*?[\ `) {
			if menu.Item(locales.ChangesIgnoreExt(ext)).Disabled(ro).Chosen() {
				*act = func() { a.ignore(t, "*"+ext) }
			}
		}
	}
	if r.sec != secConflicts && n > 0 {
		ps := paths(m.st, es)
		if menu.Item(locales.ChangesStashFiles(n)).Disabled(ro).Chosen() {
			*act = func() { a.openStashPaths(t, ps) }
		}
		if r.sec == secStaged && canStashStaged(t) && menu.Item(locales.ChangesStashStaged()).Disabled(ro).Chosen() {
			*act = func() {
				a.openStash(t)
				a.stashUI.what = 1
			}
		}
		if menu.Item(locales.ChangesSavePatch(n)).Disabled(a.SaveFile == nil).Chosen() {
			var paths []string
			for _, e := range es {
				paths = append(paths, string(m.st.Path(int(e))))
			}
			req := session.PatchSave{Paths: paths, Cached: r.sec == secStaged}
			*act = func() { a.savePatch(t, req, "changes.patch") }
		}
	}
	if r.kind == rowFile {
		menu.Separator()
		if !untrackedFolder(m.st, int(r.idx)) {
			a.fileLockMenu(menu, t, string(m.st.Path(int(r.idx))), act)
		}
		a.fileMenu(menu, t, string(m.st.Path(int(r.idx))), act)
	}
}

// toggleStage stages what is unstaged and unstages what is staged.
func (a *App) toggleStage(t *tab, m *changeModel, rows []changeRow) {
	if len(rows) == 0 || rows[0].kind == rowHeader || t.s.Queue == nil {
		return
	}
	sec := rows[0].sec
	if sec == secConflicts {
		sec = secUnstaged
	}
	t.cv.keepPlace(m, rows)
	a.stageEntries(t, m, sec, m.entries(rows))
}

// stageEntries stages entries listed under Changes or Conflicts, and
// unstages those listed under Staged.
func (a *App) stageEntries(t *tab, m *changeModel, from section, es []int32) {
	if len(es) == 0 {
		return
	}
	w := t.s.Where()
	ps := paths(m.st, es)
	op := write.Stage(w, ps)
	if from == secStaged {
		op = write.Unstage(w, ps, len(t.s.State.Head.OID) == 0)
	}
	if _, err := t.s.Submit(op); err != nil {
		a.notice = errText(err)
	}
}

func (a *App) ignore(t *tab, pattern string) {
	op, err := write.Ignore(t.s.Where(), pattern)
	if err == nil {
		_, err = t.s.Submit(op)
	}
	if err != nil {
		a.notice = errText(err)
	}
}

// askDiscard asks before discarding changes to tracked entries and
// deleting untracked ones. What the user is shown is what the write
// expects to find: HEAD and the files as they are now, read off the main
// thread.
func (a *App) askDiscard(t *tab, m *changeModel, es []int32) {
	var tracked, untracked, intent []string
	for _, e := range es {
		p := string(m.st.Path(int(e)))
		switch {
		case m.st.Kind(int(e)) == status.Untracked:
			untracked = append(untracked, p)
		case intentToAdd(m.st, int(e)):
			intent = append(intent, p)
		default:
			tracked = append(tracked, p)
		}
	}
	a.askDiscardPaths(t, &t.cv.confirm, tracked, untracked, intent)
}

// intentToAdd reports whether entry i is a file added with intent to add
// (git add -N): the index holds none of its content, so putting it back
// as it is in the index would empty it.
func intentToAdd(st *status.Status, i int) bool {
	x, y := st.XY(i)
	return st.Kind(i) == status.Changed && x == '.' && y == 'A'
}

// askDiscardPaths is askDiscard of paths, asking in confirm. intent are
// files added with intent to add, whose whole content is their change:
// the intent is taken back and the file left as it is, untracked.
func (a *App) askDiscardPaths(t *tab, confirm *confirmState, tracked, untracked, intent []string) {
	n := len(tracked) + len(untracked) + len(intent)
	w := t.s.Where()
	head := t.s.State.Head
	update := a.Update
	a.goWork(t, func() {
		// One expectation each: the discard changes the tracked files,
		// which the delete, queued after it, must not find changed.
		trackedFiles, err := write.Observe(w.WorkTree, tracked)
		var untrackedFiles []write.FileState
		if err == nil {
			untrackedFiles, err = write.Observe(w.WorkTree, untracked)
		}
		update(func() {
			if err != nil {
				a.tell(t, errText(err))
				return
			}
			expect := func(files []write.FileState) *write.Expect {
				x := &write.Expect{Head: hex.EncodeToString(head.OID), Files: files}
				if !head.Detached {
					x.Branch = head.Target
				}
				return x
			}
			names := nameList(slices.Concat(tracked, intent, untracked))
			changed := len(tracked) + len(intent)
			message := locales.ChangesDiscardBoth(changed, len(untracked), names)
			switch {
			case len(untracked) == 0:
				message = locales.ChangesDiscardTracked(changed, names)
			case changed == 0:
				message = locales.ChangesDiscardUntracked(len(untracked), names)
			}
			*confirm = confirmState{
				open:    true,
				title:   locales.ChangesDiscardTitle(n),
				message: message,
				button:  locales.ChangesDiscard(),
				run: func() {
					discard := func() { a.discardPaths(t, w, tracked, untracked, expect(trackedFiles), expect(untrackedFiles)) }
					if len(intent) == 0 {
						discard()
						return
					}
					j, err := t.s.Submit(write.Unstage(w, intent, len(head.OID) == 0))
					if err != nil {
						a.notice = errText(err)
						return
					}
					t.s.Then(j, func(err error) {
						if err == nil {
							discard()
						}
					})
				},
			}
		})
	})
}

// discardPaths discards the changes to tracked paths and then deletes
// untracked ones, each write refusing unless it finds what its
// expectation says.
func (a *App) discardPaths(t *tab, w write.Where, tracked, untracked []string, trackedX, untrackedX *write.Expect) {
	del := func() {
		if len(untracked) == 0 {
			return
		}
		op, err := write.Delete(w, untracked)
		if err == nil {
			op.Expect = untrackedX
			_, err = t.s.Submit(op)
		}
		if err != nil {
			a.notice = errText(err)
		}
	}
	if len(tracked) == 0 {
		del()
		return
	}
	op := write.Discard(w, tracked)
	op.Expect = trackedX
	j, err := t.s.Submit(op)
	if err != nil {
		a.notice = errText(err)
		return
	}
	// Only once the discard went well: a delete ending well after it
	// would clear its error from view.
	t.s.Then(j, func(err error) {
		if err == nil {
			del()
		}
	})
}

// nameList names up to five paths, and how many more.
func nameList(ps []string) string {
	const show = 5
	var b strings.Builder
	for i, p := range ps {
		if i == show {
			b.WriteString(locales.ChangesAndMore(len(ps) - show))
			break
		}
		shown, _ := showPath(p)
		b.WriteString(shown)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// confirmView shows a pending question; the safe answer is the default.
func (a *App) confirmView(c *ui.Context, q *confirmState) {
	if !q.open {
		return
	}
	if ui.AlertDialog(c, &q.open, q.title, q.message, q.button, locales.CommonCancel()) == 0 && q.run != nil {
		run := q.run
		q.run = nil
		run()
	}
}

// untrackedFolder reports whether entry i is a folder git does not
// track: git lists one with nothing tracked in it whole, its path
// ending in a slash, rather than each file in it.
func untrackedFolder(st *status.Status, i int) bool {
	p := st.Path(i)
	return st.Kind(i) == status.Untracked && len(p) > 0 && p[len(p)-1] == '/'
}

// entryColor is the colour of entry i's letter: the commit lists' for
// git's letters, new for what git does not track, danger for a conflict.
func entryColor(k ink, st *status.Status, i int, letter string) ui.Color {
	switch st.Kind(i) {
	case status.Untracked:
		return k.Success
	case status.Unmerged:
		return k.Danger
	}
	return fileStatusColor(k, letter[0])
}

// changeIcons are the change list's own icons, parsed once.
var changeIcons = sync.OnceValue(func() (icons struct{ folder *ui.SVG }) {
	icons.folder = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linejoin="round"><path d="M3.5 7.5a2 2 0 0 1 2-2h4l2 2.5h7a2 2 0 0 1 2 2v7.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/></svg>`))
	return icons
})
