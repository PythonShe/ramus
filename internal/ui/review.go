package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/git/diff"
	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
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
	// files are the status entries listed, for builtFor and onlyNew.
	files     []int32
	builtFor  *status.Status
	builtOnly bool
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
	rv.stamping = false
}

func (a *App) reviewView(c *ui.Context, t *tab) {
	th := c.Theme()
	rv := &t.rev
	st := t.s.State.Status
	rv.tick++
	rv.wanted = rv.wanted[:0]
	if st == nil {
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	}
	a.stampReview(t, st)
	if rv.builtFor != st || rv.builtOnly != rv.onlyNew {
		rv.files = reviewFiles(st, rv)
		rv.builtFor, rv.builtOnly = st, rv.onlyNew
	}
	files := rv.files
	ro := t.s.Queue == nil
	rv.list.Key = func(i int) any { return [2]any{string(st.Path(int(files[i/2]))), i % 2} }
	rv.list.Header = func(i int) bool { return i%2 == 0 }
	var act func()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			what := countFiles(len(files)) + " to review"
			if rv.onlyNew {
				what = countFiles(len(files)) + " changed since seen"
			}
			ui.Text(c, what).Bold().Grow(1)
			ui.Checkbox(c, &rv.onlyNew, "Changed Since Seen").Disabled(rv.seen == nil)
			if ui.Button(c, "Mark All Seen").Disabled(rv.stampedFor != st || rv.stamps == nil).Clicked() {
				rv.seen, rv.builtFor = rv.stamps, nil
			}
		})
		a.diffBar(c, nil)
		switch {
		case rv.onlyNew && rv.stampedFor != st:
			ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			return
		case len(files) == 0 && rv.onlyNew:
			ui.Column(c).Fill().Center().Children(func() {
				ui.Text(c, "Nothing changed since you marked it seen").TextColor(th.TextMuted)
			})
			return
		case len(files) == 0:
			ui.Column(c).Fill().Center().Children(func() {
				ui.Text(c, "Nothing to review").TextColor(th.TextMuted)
			})
			return
		}
		ui.List(c, &rv.list, 2*len(files), func(i int) {
			e := int(files[i/2])
			if i%2 == 0 {
				a.reviewHeader(c, t, st, e, ro, &act)
			} else {
				a.reviewBody(c, t, st, e, ro)
			}
		}).Label("Changes to review").Grow(1)
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

// reviewFiles are the entries of st with changes not staged, and with
// onlyNew those changed since they were marked seen.
func reviewFiles(st *status.Status, rv *reviewState) []int32 {
	var out []int32
	for i := range st.Len() {
		if !inSection(st, i, secUnstaged) {
			continue
		}
		if rv.onlyNew && rv.seen != nil {
			p := string(st.Path(i))
			if s, ok := rv.seen[p]; ok && s == rv.stamps[p] {
				continue
			}
		}
		out = append(out, int32(i))
	}
	return out
}

// stampReview reads, off the main thread, the size and time of each file
// to review, for st.
func (a *App) stampReview(t *tab, st *status.Status) {
	rv := &t.rev
	if rv.stampedFor == st || rv.stamping {
		return
	}
	var ps []string
	for i := range st.Len() {
		if inSection(st, i, secUnstaged) {
			ps = append(ps, string(st.Path(i)))
		}
	}
	if len(ps) > reviewStampMax {
		rv.stampedFor, rv.stamps = st, nil
		return
	}
	rv.stamping = true
	root, update := t.s.Where().WorkTree, a.Update
	go func() {
		stamps := make(map[string]fileStamp, len(ps))
		for _, p := range ps {
			fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil {
				stamps[p] = fileStamp{missing: true}
				continue
			}
			stamps[p] = fileStamp{size: fi.Size(), mtime: fi.ModTime().UnixNano(), mode: fi.Mode()}
		}
		update(func() {
			if !rv.stamping {
				return
			}
			rv.stamping = false
			if t.s.State.Status == st {
				rv.stamps, rv.stampedFor = stamps, st
				rv.builtFor = nil
			}
		})
	}()
}

func (a *App) reviewHeader(c *ui.Context, t *tab, st *status.Status, e int, ro bool, act *func()) {
	th := c.Theme()
	rv := &t.rev
	path := string(st.Path(e))
	it := rv.item(path)
	ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Background(th.Surface).Children(func() {
		shown, _ := showPath(path)
		ui.Text(c, shown).Bold().SingleLine().Grow(1)
		ui.Text(c, entryWords(st, e, secUnstaged)).FontSize(12).TextColor(th.TextMuted)
		lt := lineTarget{p: it.res.Parsed, path: path, pv: &it.pv}
		if picked, n := it.pv.selectedLines(); partialOK(it.res, it.key) && n > 0 {
			lines := fmt.Sprintf(" %d Lines", n)
			if n == 1 {
				lines = " 1 Line"
			}
			if ui.Button(c, "Discard"+lines+"…").Disabled(ro).Clicked() {
				*act = func() { a.askDiscardLinesOf(t, lt, &rv.confirm, picked) }
			}
			if ui.Button(c, "Keep"+lines).Disabled(ro).Clicked() {
				*act = func() { a.applyLinesOf(t, lt, picked, true, false) }
			}
		}
		if ui.Button(c, "Discard File…").Label("Discard " + shown + "…").Disabled(ro).Clicked() {
			*act = func() {
				if st.Kind(e) == status.Untracked {
					a.askDiscardPaths(t, &rv.confirm, nil, []string{path})
				} else {
					a.askDiscardPaths(t, &rv.confirm, []string{path}, nil)
				}
			}
		}
		if ui.Button(c, "Keep File").Label("Keep " + shown).Disabled(ro).Clicked() {
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
	note := func(s string) { ui.Text(c, s).TextColor(th.TextMuted).Padding(12, 16) }
	switch {
	case res.Err != nil:
		ui.Text(c, errText(res.Err)).TextColor(th.Danger).Padding(12, 16).Selectable()
	case p == nil:
		ui.Row(c).Padding(12, 16).Children(func() { ui.Spinner(c) })
	case res.Truncated:
		ui.Row(c).Padding(8, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Too large to review here").TextColor(th.TextMuted)
			if ui.Button(c, "Show in Changes").Label("Show " + path + " in Changes").Clicked() {
				t.sel, t.cv.want = "changes", path
				t.cv.findWanted()
			}
		})
	case len(p.Files) == 0 || len(p.Hunks) == 0 && !p.Files[0].Binary:
		note("No differences in content")
	case pv.sub != nil:
		note("Submodule change")
	case p.Files[0].Binary:
		note("Binary file")
	case pv.failed:
		note("This patch cannot be shown")
	case pv.d == nil:
		ui.Row(c).Padding(12, 16).Children(func() { ui.Spinner(c) })
	default:
		if pv.endings != "" {
			ui.Text(c, "Line endings change from "+pv.endings+" on every line shown").
				FontSize(12).TextColor(th.Warning).Padding(4, 12)
		}
		var actions func(c *ui.Context, change int)
		if partialOK(res, it.key) {
			lt := lineTarget{p: p, path: path, pv: pv}
			actions = func(c *ui.Context, change int) {
				lines := pv.changeLines(change)
				if ui.Button(c, "Discard…").Disabled(ro).Clicked() {
					a.askDiscardLinesOf(t, lt, &rv.confirm, lines)
				}
				if ui.Button(c, "Keep").Disabled(ro).Clicked() {
					a.applyLinesOf(t, lt, lines, true, false)
				}
			}
		}
		a.setDiffView(pv.d, actions)
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
