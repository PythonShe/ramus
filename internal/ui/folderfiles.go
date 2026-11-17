package ui

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/internal/git/repo"
	"ramus/internal/git/status"
	"ramus/internal/session"
	"ramus/locales"
)

// folderListed bounds the files of an untracked folder listed: past it
// they are only counted.
const folderListed = 5000

// folderListH is the height the list of a folder's files starts at, over
// the patch of the one chosen.
const folderListH = 200

// folderState lists the files of the untracked folder chosen in the
// changes list, read again with each status, and the one of them chosen,
// whose patch the pane shows.
type folderState struct {
	dir     string
	readFor *status.Status
	loading bool
	files   repo.UntrackedFiles
	err     string
	list    ui.ListState
	chosen  int
	listH   float32
	// cancel stops the read running.
	cancel func()
}

// leave stops the read of the folder shown and forgets it, once the pane
// shows something else.
func (f *folderState) leave() {
	if f.cancel != nil {
		f.cancel()
	}
	*f = folderState{listH: f.listH}
}

// folderFiles fills the patch's pane, for a folder git does not track,
// with the files in it and the patch of the one chosen.
func (a *App) folderFiles(c *ui.Context, t *tab, m *changeModel, r changeRow) {
	th := c.Theme()
	f := &t.cv.folder
	dir := string(m.st.Path(int(r.idx)))
	if f.dir != dir {
		f.leave()
		f.dir, f.chosen = dir, -1
	}
	if f.listH == 0 {
		f.listH = folderListH
	}
	if f.readFor != m.st {
		// Read again with each status: files come and go in the folder.
		if f.cancel != nil {
			f.cancel()
		}
		f.readFor, f.loading = m.st, true
		f.cancel = t.s.UntrackedIn(dir, folderListed, func(files repo.UntrackedFiles, err error) {
			was := ""
			if f.chosen >= 0 && f.chosen < len(f.files.Paths) {
				was = f.files.Paths[f.chosen]
			}
			f.loading, f.cancel, f.files, f.err = false, nil, files, ""
			if err != nil {
				f.err = errText(err)
			}
			// The file chosen stays chosen where it still is; else the
			// first is, so the patch's pane is never blank.
			f.chosen = slices.Index(files.Paths, was)
			if f.chosen < 0 && len(files.Paths) > 0 {
				f.chosen = 0
			}
			f.list.Choose(f.chosen)
		})
	}
	ro := t.s.Queue == nil
	shownDir, _ := showPath(dir)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).MinHeight(barHeight).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			pathLabel(c, shownDir).Bold().Grow(1)
			if n := len(f.files.Paths); n > 0 {
				count := locales.ChangesFolderFiles(n)
				if f.files.More > 0 {
					count = locales.ChangesFolderFirstFiles(n, n+f.files.More)
				}
				ui.Text(c, count).FontSize(textSmall).TextColor(th.TextMuted)
			}
			if ui.Button(c, locales.ChangesDiscardFolder()).Disabled(ro).Clicked() {
				a.discardIn(t, m.st, r.sec, []int32{r.idx})
			}
			if ui.Button(c, locales.ChangesStageFolder()).Disabled(ro).Clicked() {
				a.toggleStage(t, m, []changeRow{r})
			}
		})
		switch {
		case f.err != "":
			errorMessage(c, f.err)
		case f.loading && f.files.Paths == nil:
			loading(c, "")
		case len(f.files.Paths) == 0:
			emptyState(c, locales.ChangesUntrackedFolder(shownDir))
		default:
			ui.SplitVertical(c, &f.listH, func() { a.folderList(c, f, shownDir) }, func() { a.folderPatch(c, t, m, f) }).SplitReset(folderListH).Fill()
		}
	})
}

func (a *App) folderList(c *ui.Context, f *folderState, shownDir string) {
	paths, dir := f.files.Paths, f.dir
	f.list.Selected = &f.chosen
	f.list.Key = func(i int) any { return paths[i] }
	f.list.Label = func(i int) string { shown, _ := showPath(paths[i]); return shown }
	ui.List(c, &f.list, len(paths)).Rows(func(lr ui.ListRow) {
		k := rowInk(lr)
		p := paths[lr.Index]
		ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
			fileIcon(c, p, k)
			// Named from the folder, which the bar above names.
			shown, why := showPath(strings.TrimPrefix(p, dir))
			txt := pathLabel(c, shown).Grow(1)
			if why != "" {
				txt.TextColor(k.Warning).Tooltip(why)
			}
		})
	}).Label(locales.ChangesFolderFilesLabel(shownDir)).Fill()
}

// folderPatch shows the patch of the file of the folder chosen: all of it
// added.
func (a *App) folderPatch(c *ui.Context, t *tab, m *changeModel, f *folderState) {
	if f.chosen < 0 || f.chosen >= len(f.files.Paths) {
		emptyPane(c)
		return
	}
	p := f.files.Paths[f.chosen]
	if strings.HasSuffix(p, "/") {
		// A repository inside, which git lists as a folder: no patch.
		emptyPane(c)
		return
	}
	key := diffKey{path: p, src: diff.Untracked, st: m.st, ws: diff.Whitespace(a.diffOpts().ws)}
	a.readPatch(t, key, session.Sides{WorkTreePath: p})
	d := &t.cv.diff
	ui.Column(c).Fill().Children(func() {
		a.patchBar(c, false, !binaryPatch(d.res), func() {
			if ui.Button(c, locales.ActionsOpenInEditor()).Disabled(!a.canOpenInEditor(t)).Clicked() {
				a.openInEditor(t, p)
			}
		})
		a.pagerNotice(c, &d.pager, d.res, d.loading, locales.PatchShownInPartNoStaging(sizeText(d.pager.max())), d.load)
		res := d.res
		if d.loading && res.Parsed == nil {
			res = session.PatchResult{}
		}
		a.patchBody(c, t, &d.pv, res, nil)
	})
}
