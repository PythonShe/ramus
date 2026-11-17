package ui

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/git/diff"
	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// Bounds on the review: the patch and whole sides read for one file, the
// bytes and files of patches kept for files scrolled past, and the files
// whose times are read for "changed since seen".
const (
	reviewPatchMax = 1 << 20
	reviewSideMax  = 4 << 20
	reviewKeepMax  = 64 << 20
	reviewKeepN    = 128
	reviewStampMax = 50000
)

// reviewState is the review of every change not staged yet: one list of
// the files' patches, each kept (staged) or discarded by the change or
// whole. A file kept leaves the list.
type reviewState struct {
	list ui.ListState
	// files are the entries of filesOf listed. They are made off the main
	// thread, for builtFor, builtOnly and builtGen; gen counts the
	// changes to seen and stamps.
	files     []int32
	filesOf   *status.Status
	building  bool
	builtFor  *status.Status
	builtOnly bool
	builtGen  int
	gen       int
	// items are the patches read, by path; tick counts the frames shown,
	// which an item's used is set to when built.
	items   map[string]*reviewItem
	tick    uint64
	loading *reviewItem
	wanted  []reviewWant
	slot    session.PatchSlot
	confirm confirmState
	// seen are the files as they were when marked seen; stamps as they
	// are, read for stampedFor. onlyNew lists the files changed since.
	seen       map[string]fileStamp
	stamps     map[string]fileStamp
	stampedFor *status.Status
	stamping   bool
	onlyNew    bool
}

// reviewItem is one file's patch in the review.
type reviewItem struct {
	key  diffKey
	res  session.PatchResult
	pv   patchView
	used uint64
}

// reviewWant is a file shown whose patch is not read for what it is now.
type reviewWant struct {
	it    *reviewItem
	key   diffKey
	sides session.Sides
}

// fileStamp is what a file's change is told by without reading it.
type fileStamp struct {
	size, mtime int64
	mode        os.FileMode
	missing     bool
}

func (rv *reviewState) item(path string) *reviewItem {
	if rv.items == nil {
		rv.items = map[string]*reviewItem{}
	}
	it := rv.items[path]
	if it == nil {
		it = &reviewItem{}
		rv.items[path] = it
	}
	return it
}

// leave drops the patch being read, which the session cancels as the
// view is left.
func (rv *reviewState) leave() {
	if it := rv.loading; it != nil {
		it.key = diffKey{}
		rv.loading = nil
	}
	rv.stamping, rv.building = false, false
}

func (a *App) reviewView(c *ui.Context, t *tab) {
	rv := &t.rev
	st := t.s.State.Status
	rv.tick++
	rv.wanted = rv.wanted[:0]
	if st == nil {
		loading(c, "")
		return
	}
	a.stampReview(t, st)
	a.rebuildReview(t, st)
	t.cv.countLines(t)
	cur := st
	if st = rv.filesOf; st == nil {
		loading(c, "")
		return
	}
	files := rv.files
	ro := t.s.Queue == nil
	rv.list.Key = func(i int) any { return [2]any{string(st.Path(int(files[i/2]))), i % 2} }
	rv.list.Header = func(i int) bool { return i%2 == 0 }
	var act func()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			changesModes(c, &t.cv)
			what := locales.ReviewToReview(len(files))
			if rv.onlyNew {
				what = locales.ReviewChangedSinceSeenCount(len(files))
			}
			ui.Text(c, what).Bold().Grow(1)
			ui.Checkbox(c, &rv.onlyNew, locales.ReviewChangedSinceSeen()).Disabled(rv.seen == nil)
			if ui.Button(c, locales.ReviewMarkAllSeen()).Disabled(rv.stampedFor != cur || rv.stamps == nil).Clicked() {
				rv.seen = rv.stamps
				rv.gen++
			}
		})
		a.diffBar(c, true, nil)
		switch {
		case rv.onlyNew && rv.stampedFor != cur, rv.builtOnly != rv.onlyNew:
			loading(c, "")
			return
		case len(files) == 0 && rv.onlyNew:
			emptyState(c, locales.ReviewNothingSinceSeen())
			return
		case len(files) == 0:
			emptyState(c, locales.ReviewNothing())
			return
		}
		ui.List(c, &rv.list, 2*len(files), func(i int) {
			e := int(files[i/2])
			if i%2 == 0 {
				a.reviewHeader(c, t, st, e, ro, &act)
			} else {
				a.reviewBody(c, t, st, e, ro)
			}
		}).Label(locales.ReviewChanges()).Grow(1)
	})
	a.confirmView(c, &rv.confirm)
	if act != nil {
		act()
	}
	if rv.loading == nil && len(rv.wanted) > 0 {
		a.loadReview(t, rv.wanted[0])
	}
	rv.trim()
}

// rebuildReview lists the files to review again, off the main thread,
// when the status, the choice of only new files, or what was seen
// changed. Main thread only.
func (a *App) rebuildReview(t *tab, st *status.Status) {
	rv := &t.rev
	if rv.building || rv.builtFor == st && rv.builtOnly == rv.onlyNew && rv.builtGen == rv.gen {
		return
	}
	rv.building = true
	onlyNew, gen := rv.onlyNew, rv.gen
	// seen and stamps are replaced, never changed in place, so the
	// goroutine may read them.
	seen, stamps, update := rv.seen, rv.stamps, a.Update
	a.goWork(t, func() {
		files := reviewFiles(st, onlyNew, seen, stamps)
		update(func() {
			if !rv.building {
				return
			}
			rv.building = false
			rv.files, rv.filesOf = files, st
			rv.builtFor, rv.builtOnly, rv.builtGen = st, onlyNew, gen
		})
	})
}

// reviewFiles are the entries of st with changes not staged, and with
// onlyNew those changed since they were marked seen.
func reviewFiles(st *status.Status, onlyNew bool, seen, stamps map[string]fileStamp) []int32 {
	var out []int32
	for i := range st.Len() {
		if !inSection(st, i, secUnstaged) {
			continue
		}
		if onlyNew && seen != nil {
			p := string(st.Path(i))
			if s, ok := seen[p]; ok && s == stamps[p] {
				continue
			}
		}
		out = append(out, int32(i))
	}
	return out
}

// stampReview reads, off the main thread, the size and time of each file
// to review, for st, unless there are more than reviewStampMax.
func (a *App) stampReview(t *tab, st *status.Status) {
	rv := &t.rev
	if rv.stampedFor == st || rv.stamping {
		return
	}
	rv.stamping = true
	root, update := t.s.Where().WorkTree, a.Update
	a.goWork(t, func() {
		n := 0
		for i := range st.Len() {
			if inSection(st, i, secUnstaged) {
				n++
			}
		}
		var stamps map[string]fileStamp
		if n <= reviewStampMax {
			stamps = stampFiles(root, st, n)
		}
		update(func() {
			if !rv.stamping {
				return
			}
			rv.stamping = false
			if t.s.State.Status == st {
				rv.stamps, rv.stampedFor = stamps, st
				rv.gen++
			}
		})
	})
}

// stampFiles reads the size and time of the n files of st to review.
func stampFiles(root string, st *status.Status, n int) map[string]fileStamp {
	stamps := make(map[string]fileStamp, n)
	for i := range st.Len() {
		if !inSection(st, i, secUnstaged) {
			continue
		}
		p := string(st.Path(i))
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			stamps[p] = fileStamp{missing: true}
			continue
		}
		stamps[p] = fileStamp{size: fi.Size(), mtime: fi.ModTime().UnixNano(), mode: fi.Mode()}
	}
	return stamps
}

func (a *App) reviewHeader(c *ui.Context, t *tab, st *status.Status, e int, ro bool, act *func()) {
	th := c.Theme()
	rv := &t.rev
	path := string(st.Path(e))
	it := rv.item(path)
	ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		shown, _ := showPath(path)
		pathLabel(c, shown).Bold().Grow(1)
		ui.Text(c, entryWords(st, e, secUnstaged)).FontSize(textSmall).TextColor(th.TextMuted)
		fileLines(c, t.cv.entryLines(st, e, secUnstaged))
		lt := lineTarget{p: it.res.Parsed, path: path, pv: &it.pv}
		if picked, n := it.pv.selectedLines(); partialOK(it.res, it.key) && n > 0 {
			if linesOK(it.res, true) && ui.Button(c, locales.ReviewDiscardLines(n)).Disabled(ro).Clicked() {
				*act = func() { a.askDiscardLinesOf(t, lt, &rv.confirm, picked) }
			}
			if linesOK(it.res, false) && ui.Button(c, locales.ReviewKeepLines(n)).Disabled(ro).Clicked() {
				*act = func() { a.applyLinesOf(t, lt, picked, true, false) }
			}
		}
		if ui.Button(c, locales.ReviewDiscardFile()).Label(locales.ReviewDiscardPath(shown)).Disabled(ro).Clicked() {
			*act = func() {
				switch {
				case st.Kind(e) == status.Untracked:
					a.askDiscardPaths(t, &rv.confirm, discardSet{untracked: []string{path}})
				case intentToAdd(st, e):
					a.askDiscardPaths(t, &rv.confirm, discardSet{intent: []string{path}})
				default:
					a.askDiscardPaths(t, &rv.confirm, discardSet{tracked: []string{path}})
				}
			}
		}
		if ui.Button(c, locales.ReviewKeepFile()).Label(locales.ReviewKeepPath(shown)).Disabled(ro).Clicked() {
			ps := paths(st, []int32{int32(e)})
			*act = func() {
				if _, err := t.s.Submit(write.Stage(t.s.Where(), ps)); err != nil {
					a.notice = errText(err)
				}
			}
		}
	})
}

func (a *App) reviewBody(c *ui.Context, t *tab, st *status.Status, e int, ro bool) {
	th := c.Theme()
	rv := &t.rev
	path := string(st.Path(e))
	it := rv.item(path)
	it.used = rv.tick
	key := diffKey{path: path, src: diff.Unstaged, st: st, ws: diff.Whitespace(a.diffOpts().ws)}
	if st.Kind(e) == status.Untracked {
		key.src = diff.Untracked
	}
	key.orig = renamedFrom(st, e, key.src)
	if it.key != key && rv.loading != it {
		sides := patchSides(st, e, key.src)
		sides.Max = reviewSideMax
		rv.wanted = append(rv.wanted, reviewWant{it: it, key: key, sides: sides})
	}
	res, p, pv := it.res, it.res.Parsed, &it.pv
	note := func(s string) { ui.Text(c, s).TextColor(th.TextMuted).Padding(spaceL, insetX) }
	switch {
	case res.Err != nil:
		errorMessage(c, errText(res.Err))
	case p == nil:
		ui.Row(c).Padding(spaceL, insetX).Children(func() { ui.Spinner(c) })
	case res.Truncated:
		ui.Row(c).Padding(spaceM, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.ReviewTooLarge()).TextColor(th.TextMuted)
			if ui.Button(c, locales.ReviewShowInChanges()).Label(locales.ReviewShowPathInChanges(path)).Clicked() {
				t.sel, t.cv.want = "changes", path
				t.cv.findWanted()
			}
		})
	case len(p.Files) == 0 || len(p.Hunks) == 0 && !p.Files[0].Binary:
		note(locales.ReviewNoDifferences())
	case pv.sub != nil:
		note(locales.ReviewSubmoduleChange())
	case p.Files[0].Binary:
		note(locales.ReviewBinary())
	case pv.failed:
		note(locales.ReviewCannotShow())
	case pv.d == nil:
		ui.Row(c).Padding(spaceL, insetX).Children(func() { ui.Spinner(c) })
	default:
		if pv.endings != "" {
			ui.Text(c, locales.ReviewLineEndings(pv.endings)).
				FontSize(textSmall).TextColor(th.Warning).Padding(rowPadY, insetX)
		}
		var actions func(c *ui.Context, change int)
		if partialOK(res, it.key) {
			lt := lineTarget{p: p, path: path, pv: pv}
			actions = func(c *ui.Context, change int) {
				lines := pv.changeLines(change)
				if ui.Button(c, locales.ReviewDiscardChange()).Disabled(ro).Clicked() {
					a.askDiscardLinesOf(t, lt, &rv.confirm, lines)
				}
				if ui.Button(c, locales.ReviewKeep()).Disabled(ro).Clicked() {
					a.applyLinesOf(t, lt, lines, true, false)
				}
			}
		}
		a.setDiffView(pv.d, true, actions)
		if wholeFile(p) {
			pv.d.Layout = editor.DiffUnified
		}
		pv.d.FitContent = true
		editor.DiffView(c, pv.d).FillWidth()
	}
}

// loadReview reads the patch a file shown wants, one at a time.
func (a *App) loadReview(t *tab, w reviewWant) {
	rv := &t.rev
	it, key := w.it, w.key
	rv.loading, it.key = it, key
	req := diff.PatchRequest{Source: key.src, Paths: key.paths(), Context: -1, MaxBytes: reviewPatchMax, Whitespace: key.ws, Renames: key.orig != ""}
	t.s.PatchIn(&rv.slot, req, w.sides, func(res session.PatchResult) {
		if rv.loading == it {
			rv.loading = nil
		}
		if it.key != key {
			return
		}
		it.res = res
		a.setPatch(&it.pv, res)
	})
}

// trim lets go of the patches of files not shown this frame, the least
// recently shown first, past the bounds.
func (rv *reviewState) trim() {
	size := func(it *reviewItem) int {
		n := len(it.res.Old) + len(it.res.New)
		if p := it.res.Parsed; p != nil {
			n += len(p.Text)
		}
		return n
	}
	total := 0
	for _, it := range rv.items {
		total += size(it)
	}
	if total <= reviewKeepMax && len(rv.items) <= reviewKeepN {
		return
	}
	type aged struct {
		path string
		used uint64
	}
	var old []aged
	for p, it := range rv.items {
		if it.used != rv.tick && it != rv.loading {
			old = append(old, aged{p, it.used})
		}
	}
	slices.SortFunc(old, func(x, y aged) int {
		switch {
		case x.used < y.used:
			return -1
		case x.used > y.used:
			return 1
		}
		return 0
	})
	for _, o := range old {
		if total <= reviewKeepMax && len(rv.items) <= reviewKeepN {
			break
		}
		total -= size(rv.items[o.path])
		delete(rv.items, o.path)
	}
}
