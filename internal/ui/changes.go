package ui

import (
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
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
	mode        int // 0 list, 1 tree
	open        map[openKey]bool
	openGen     int
	list        ui.ListState
	chosen      int
	picked      ui.Selection[rowKey]
	confirm     confirmState
	diff        diffState
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
	tree := cv.mode == 1
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
	go func() {
		m := buildChanges(st, filter, tree, open)
		update(func() {
			cv.building = false
			cv.model, cv.builtFor, cv.builtFilter, cv.builtTree, cv.builtOpen = m, st, filter, tree, gen
		})
	}()
}

func (a *App) changesList(c *ui.Context, t *tab) {
	th := c.Theme()
	cv := &t.cv
	a.rebuildChanges(t)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.SearchField(c, &cv.filter).Placeholder("Filter by path").Grow(1)
			ui.Segmented(c, &cv.mode, "List", "Tree")
			ro := t.s.Queue == nil
			ui.PopoverMenuButton(c, "More", func(m *ui.Menu) {
				if m.Item("Stash Changes…").Disabled(ro).Chosen() {
					a.openStash(t)
				}
				if m.Item("Clean Untracked Files…").Disabled(ro).Chosen() {
					a.openClean(t)
				}
				if m.Item("Apply Patch File…").Disabled(ro || a.PickFile == nil).Chosen() {
					a.applyPatchFile(t)
				}
			})
		})
		m := cv.model
		if m == nil {
			ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			return
		}
		if len(m.rows) == 0 {
			ui.Column(c).Fill().Center().Children(func() {
				msg := "No changes"
				if m.filter != "" {
					msg = "No changed path holds “" + m.filter + "”"
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
		cv.list.Label = func(i int) string { return rowLabel(m, rows[i]) }
		var act func()
		l := ui.List(c, &cv.list, len(rows), func(i int) { a.changeRow(c, t, m, rows[i], &act) }).Grow(1)
		if l.Submitted() && cv.chosen >= 0 && cv.chosen < len(rows) {
			r := rows[cv.chosen]
			act = func() { a.toggleStage(t, m, []changeRow{r}) }
		}
		if act != nil {
			act()
		}
	})
	a.confirmView(c, &cv.confirm)
}

// changesPane is the changes list with the commit editor under it.
func (a *App) changesPane(c *ui.Context, t *tab) {
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Grow(1).Children(func() { a.changesList(c, t) })
		a.commitBox(c, t)
	})
}

func rowLabel(m *changeModel, r changeRow) string {
	switch r.kind {
	case rowHeader:
		return fmt.Sprintf("%s, %d files", r.sec.title(), len(m.files[r.sec]))
	case rowDir:
		return "Folder " + m.name(r)
	}
	return entryWords(m.st, int(r.idx), r.sec) + " " + string(m.st.Path(int(r.idx)))
}

func (a *App) changeRow(c *ui.Context, t *tab, m *changeModel, r changeRow, act *func()) {
	th := c.Theme()
	cv := &t.cv
	ro := t.s.Queue == nil
	if r.kind == rowHeader {
		ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).FillWidth().Background(th.Surface).Children(func() {
			ui.Text(c, fmt.Sprintf("%s (%d)", r.sec.title(), len(m.files[r.sec]))).Bold().Grow(1)
			var label string
			switch r.sec {
			case secStaged:
				label = "Unstage All"
			case secUnstaged:
				label = "Stage All"
			}
			if label != "" && ui.Button(c, label).Disabled(ro).Clicked() {
				sec := r.sec
				*act = func() { a.stageEntries(t, m, sec, m.files[sec]) }
			}
		})
		return
	}
	indent := float32(r.depth) * 16
	row := ui.Row(c).Padding(3, 8).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Box(c).Width(indent)
		if r.kind == rowDir {
			k := openKey{r.sec, m.dir(r.sec, r.idx)}
			arrow := "▸"
			if cv.open[k] {
				arrow = "▾"
			}
			if ui.Text(c, arrow).Width(12).TextColor(th.TextMuted).RowClicked() {
				*act = func() { cv.open[k] = !cv.open[k]; cv.openGen++ }
			}
			ui.Text(c, m.name(r)+"/").SingleLine().Grow(1)
			return
		}
		i := int(r.idx)
		ui.Text(c, entryLetter(m.st, i, r.sec)).Font("monospace").TextColor(th.TextMuted).Width(16).Description(entryWords(m.st, i, r.sec))
		shown, flagged := showPath(m.name(r))
		if f, why := showPath(string(m.st.Path(i))); why != "" {
			shown, flagged = f, why
		}
		name := ui.Text(c, shown).SingleLine().Grow(1)
		if flagged != "" {
			name.TextColor(th.Warning).Description(flagged)
		}
		if note := entryNote(m.st, i, r.sec); note != "" {
			ui.Text(c, note).FontSize(12).TextColor(th.TextMuted).SingleLine()
		}
	})
	if row.RowDoubleClicked() {
		*act = func() { a.toggleStage(t, m, []changeRow{r}) }
	}
	row.ContextMenu(func(menu *ui.Menu) { a.changeMenu(menu, t, m, r, act) })
}

// entryLetter is the status letter shown for entry i in sec.
func entryLetter(st *status.Status, i int, sec section) string {
	switch st.Kind(i) {
	case status.Untracked:
		return "?"
	case status.Unmerged:
		return "U"
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
		return "untracked"
	case status.Unmerged:
		return "conflicted"
	}
	var what string
	switch entryLetter(st, i, sec) {
	case "M":
		what = "modified"
	case "A":
		what = "added"
	case "D":
		what = "deleted"
	case "R":
		what = "renamed"
	case "C":
		what = "copied"
	case "T":
		what = "type changed"
	default:
		what = "changed"
	}
	if sec == secStaged {
		return "staged " + what
	}
	return what
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
		notes = append(notes, "from "+string(st.OrigPath(i)))
	}
	switch {
	case to == modeGitlink || from == modeGitlink:
		notes = append(notes, submoduleNote(st.Submodule(i)))
	case to == modeSymlink || from == modeSymlink:
		if from != to && from != 0 && to != 0 {
			notes = append(notes, "now a "+modeName(to))
		} else {
			notes = append(notes, "symlink")
		}
	case from != to && from != 0 && to != 0:
		notes = append(notes, modeName(from)+" → "+modeName(to))
	}
	return strings.Join(notes, ", ")
}

func modeName(m uint32) string {
	switch m {
	case modeSymlink:
		return "symlink"
	case modeGitlink:
		return "submodule"
	case modeExec:
		return "executable"
	case modeFile:
		return "file"
	}
	return fmt.Sprintf("mode %o", m)
}

// submoduleNote reads porcelain v2's S<c><m><u>.
func submoduleNote(s string) string {
	if len(s) != 4 || s[0] != 'S' {
		return "submodule"
	}
	var what []string
	if s[1] == 'C' {
		what = append(what, "new commits")
	}
	if s[2] == 'M' {
		what = append(what, "modified content")
	}
	if s[3] == 'U' {
		what = append(what, "untracked content")
	}
	if len(what) == 0 {
		return "submodule"
	}
	return "submodule: " + strings.Join(what, ", ")
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
	what := "File"
	if n != 1 {
		what = fmt.Sprintf("%d Files", n)
	}
	switch r.sec {
	case secStaged:
		if menu.Item("Unstage " + what).Disabled(ro || n == 0).Chosen() {
			*act = func() { a.stageEntries(t, m, r.sec, es) }
		}
	case secUnstaged:
		if menu.Item("Stage " + what).Disabled(ro || n == 0).Chosen() {
			*act = func() { a.stageEntries(t, m, r.sec, es) }
		}
		if menu.Item("Discard Changes to " + what + "…").Disabled(ro || n == 0).Danger().Chosen() {
			*act = func() { a.askDiscard(t, m, es) }
		}
	case secConflicts:
		if menu.Item("Mark " + what + " Resolved").Disabled(ro || n == 0).Chosen() {
			*act = func() { a.stageEntries(t, m, secUnstaged, es) }
		}
	}
	if r.kind == rowFile && r.sec != secStaged {
		p := string(m.st.Path(int(r.idx)))
		menu.Separator()
		if menu.Item("Ignore " + p).Disabled(ro).Chosen() {
			*act = func() { a.ignore(t, write.IgnorePattern(p)) }
		}
		if ext := path.Ext(p); ext != "" && !strings.ContainsAny(ext, `*?[\ `) {
			if menu.Item("Ignore All *" + ext + " Files").Disabled(ro).Chosen() {
				*act = func() { a.ignore(t, "*"+ext) }
			}
		}
	}
	if r.sec != secConflicts && n > 0 {
		if menu.Item("Save Changes to " + what + " as Patch…").Disabled(a.SaveFile == nil).Chosen() {
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
	var tracked, untracked []string
	for _, e := range es {
		p := string(m.st.Path(int(e)))
		if m.st.Kind(int(e)) == status.Untracked {
			untracked = append(untracked, p)
		} else {
			tracked = append(tracked, p)
		}
	}
	if len(untracked) > write.MaxDelete {
		a.notice = fmt.Sprintf("Choose at most %d untracked files to delete at once", write.MaxDelete)
		return
	}
	w := t.s.Where()
	head := t.s.State.Head
	update := a.Update
	go func() {
		files, err := write.Observe(w.WorkTree, append(append([]string(nil), tracked...), untracked...))
		update(func() {
			if err != nil {
				a.notice = errText(err)
				return
			}
			expect := &write.Expect{Head: hex.EncodeToString(head.OID), Files: files}
			if !head.Detached {
				expect.Branch = head.Target
			}
			var parts []string
			if len(tracked) > 0 {
				parts = append(parts, "discard the changes to "+countFiles(len(tracked)))
			}
			if len(untracked) > 0 {
				parts = append(parts, "delete "+countFiles(len(untracked))+" Git does not track")
			}
			t.cv.confirm = confirmState{
				open:    true,
				title:   "Discard " + countFiles(len(es)) + "?",
				message: "This will " + strings.Join(parts, " and ") + ":\n" + nameList(append(tracked, untracked...)) + "\n\nA snapshot is kept: Undo or the Snapshots view brings them back.",
				button:  "Discard",
				run: func() {
					if len(tracked) > 0 {
						op := write.Discard(w, tracked)
						op.Expect = expect
						if _, err := t.s.Submit(op); err != nil {
							a.notice = errText(err)
						}
					}
					if len(untracked) > 0 {
						op, err := write.Delete(w, untracked)
						if err == nil {
							op.Expect = expect
							_, err = t.s.Submit(op)
						}
						if err != nil {
							a.notice = errText(err)
						}
					}
				},
			}
		})
	}()
}

func countFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// nameList names up to five paths, and how many more.
func nameList(ps []string) string {
	const show = 5
	var b strings.Builder
	for i, p := range ps {
		if i == show {
			fmt.Fprintf(&b, "and %d more", len(ps)-show)
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
	if ui.AlertDialog(c, &q.open, q.title, q.message, q.button, "Cancel") == 0 && q.run != nil {
		run := q.run
		q.run = nil
		run()
	}
}
