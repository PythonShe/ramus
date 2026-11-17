package ui

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/git/diff"
	"ramus/internal/git/redact"
	"ramus/internal/session"
	"ramus/locales"
)

// diffOptions are how every patch shows: the layouts and wrapping are
// kept in the settings, white space for the session. The review has a
// layout of its own (reviewSplit): it has the window's width, where a
// patch beside a list may not.
type diffOptions struct {
	split, reviewSplit int // 0 unified, 1 side by side
	// noWrap keeps each line of a patch on one line; they wrap unless
	// the user asks.
	noWrap bool
	ws     int // a diff.Whitespace
}

// layout is the layout of the review's patches, or of the others.
func (o *diffOptions) layout(review bool) *int {
	if review {
		return &o.reviewSplit
	}
	return &o.split
}

func (a *App) diffOpts() *diffOptions {
	o := &a.diff
	if !o.loaded {
		o.loaded = true
		if a.Settings.DiffSplit {
			o.split = 1
		}
		if !a.Settings.ReviewUnified {
			o.reviewSplit = 1
		}
		o.noWrap = a.Settings.DiffNoWrap
	}
	return &o.diffOptions
}

// diffBar is a row of the options of the patch shown: white space,
// wrapping and layout, the review's or the others', then what more
// builds. A change of layout or wrapping is saved in the settings.
func (a *App) diffBar(c *ui.Context, review bool, more func()) {
	a.patchBar(c, review, true, more)
}

// patchBar is diffBar, with the options of text left out unless text: a
// binary file's patch has no lines for them to change.
func (a *App) patchBar(c *ui.Context, review, text bool, more func()) {
	o := a.diffOpts()
	// A toolbar: what does not fit a narrow patch goes into its menu. Its
	// buttons pad their text by the theme's 2.5 steps, so that the first
	// one's text starts at insetX, under the file's name above it.
	ui.Toolbar(c, func() {
		if !text {
			ui.Spacer(c)
			if more != nil {
				more()
			}
			return
		}
		ws := []string{locales.DiffAllWhiteSpace(), locales.DiffIgnoreSpaceChanges(), locales.DiffIgnoreAllSpace()}
		ui.PopoverMenuButton(c, ws[o.ws], func(m *ui.Menu) {
			for i, label := range ws {
				if m.Item(label).Checked(o.ws == i).Chosen() {
					o.ws = i
				}
			}
		})
		wrap := ui.Checkbox(c, &o.noWrap, locales.DiffNoWrap())
		layout := ui.Segmented(c, o.layout(review), locales.DiffUnified(), locales.DiffSplit())
		ui.Spacer(c)
		if more != nil {
			more()
		}
		if wrap.Changed() || layout.Changed() {
			a.Settings.DiffSplit, a.Settings.ReviewUnified, a.Settings.DiffNoWrap = o.split == 1, o.reviewSplit == 0, o.noWrap
			a.keepSettingsOr(locales.DiffSettingsNotSaved)
		}
	}).Label(locales.DiffOptions()).Padding(0, insetX, spaceS, insetX-c.Theme().Space(2.5))
}

// patchView is one file's patch in mygo's diff viewer, with what maps the
// viewer's lines and changes back to the parsed patch.
type patchView struct {
	gen int
	// d is nil until the viewer is built, off the main thread.
	d     *editor.DiffState
	p     *diff.Parsed
	whole bool // the viewer has the whole files, numbered as they are
	runs  []diff.Span
	// oldAt and newAt are the line in p of each line of the viewer's
	// sides, -1 for none.
	oldAt, newAt []int32
	// endings says a change of line endings, "" for none.
	endings string
	// failed is a patch the viewer could not take.
	failed bool
	// scrollTo is where the viewer it replaced was scrolled, for the same
	// file read again, which the viewer takes once it has laid out its
	// lines; -1 for none.
	scrollTo float64
	shown    bool
	// images are a binary file's sides as pictures, nil where a side is
	// none or not a picture, and what each says of itself.
	images [2]ui.ImageSource
	about  [2]string
	// lfs is a picture stored in Git LFS, shown as pictures once
	// pictured, whatever git made of its pointers; notFetched are the
	// sides whose content was never downloaded.
	lfs, pictured bool
	notFetched    [2]bool
	// sub is a submodule's change, shown as the commits it moves over.
	sub *subChange
}

// subChange is a submodule's change: its commits before and after, and
// the commits between them once read.
type subChange struct {
	path     string
	old, new []byte
	dirty    bool
	asked    bool
	rng      *session.SubmoduleRange
	list     ui.ListState
}

// setPatch builds the viewer of res off the main thread: with the whole
// files when res has them and the patch makes one from the other, so the
// lines around its hunks unfold, else of the patch alone.
func (a *App) setPatch(pv *patchView, res session.PatchResult) {
	if res.Parsed != nil && res.Parsed == pv.p {
		// A refresh that left the file as it was hands back the patch
		// kept; the viewer, its scroll and its selection stay.
		return
	}
	pv.gen++
	gen, update := pv.gen, a.Update
	scroll := -1.0
	if pv.d != nil && pv.p != nil && res.Parsed != nil && len(pv.p.Files) > 0 && len(res.Parsed.Files) > 0 &&
		pv.p.Path(0) == res.Parsed.Path(0) {
		scroll = pv.d.ScrollY()
	}
	pv.d, pv.p, pv.failed, pv.images, pv.about, pv.sub = nil, res.Parsed, false, [2]ui.ImageSource{}, [2]string{}, nil
	pv.lfs, pv.pictured, pv.notFetched = false, false, [2]bool{}
	p := res.Parsed
	if p == nil || len(p.Files) == 0 {
		return
	}
	if old, new, dirty, ok := p.Submodule(0); ok {
		pv.sub = &subChange{path: p.Path(0), old: old, new: new, dirty: dirty}
		return
	}
	// git diffs a picture in LFS as its pointers, as text.
	lfsPicture := res.LFS && res.HasSides && isPicture(p.Path(0))
	if p.Files[0].Binary || lfsPicture {
		pv.lfs, pv.notFetched = lfsPicture, res.NotFetched
		if res.HasSides && isPicture(p.Path(0)) {
			a.goWork(nil, func() {
				var imgs [2]ui.ImageSource
				var about [2]string
				for k, data := range [][]byte{res.Old, res.New} {
					if !res.NotFetched[k] {
						imgs[k], about[k] = decodePicture(data)
					}
				}
				update(func() {
					if pv.gen == gen {
						pv.images, pv.about, pv.pictured = imgs, about, true
					}
				})
			})
		}
		return
	}
	a.goWork(nil, func() {
		next := patchView{p: p, d: &editor.DiffState{Indicators: editor.DiffClassic}, scrollTo: scroll}
		fps, err := editor.ParsePatch(string(p.Text))
		if err != nil || len(fps) == 0 {
			update(func() {
				if pv.gen == gen {
					pv.failed = true
				}
			})
			return
		}
		// The sides of a file in LFS are its content, not the pointers
		// the patch is of.
		if res.HasSides && !res.LFS {
			next.whole = next.d.SetPatchFiles(fps[0], string(res.Old), string(res.New)) == nil
		}
		if !next.whole {
			next.d.SetPatch(fps[0])
		}
		next.runs = p.Runs(0)
		next.oldAt, next.newAt = p.SideLines(0, next.whole)
		next.endings = p.LineEndings(0)
		update(func() {
			if pv.gen == gen {
				next.gen = gen
				*pv = next
			}
		})
	})
}

// changeLines is the lines of the viewer's change i, as Select takes
// them.
func (pv *patchView) changeLines(i int) func(int) bool {
	if i < 0 || i >= len(pv.runs) {
		return func(int) bool { return false }
	}
	r := pv.runs[i]
	return func(l int) bool { return uint32(l) >= r.Start && uint32(l) < r.End }
}

// selectedLines is the changed lines of the lines selected in the
// viewer, and how many.
func (pv *patchView) selectedLines() (func(int) bool, int) {
	if pv.d == nil {
		return nil, 0
	}
	r, ok := pv.d.SelectedLines()
	if !ok {
		return nil, 0
	}
	side, kind := pv.newAt, byte('+')
	if r.Side == editor.DiffOld {
		side, kind = pv.oldAt, '-'
	}
	set := map[int]bool{}
	for l := max(r.Start, 0); l <= r.End && l < len(side); l++ {
		if k := side[l]; k >= 0 && pv.p.Lines[k].Kind == kind {
			set[int(k)] = true
		}
	}
	return func(i int) bool { return set[i] }, len(set)
}

func (pv *patchView) clearSelection() {
	if pv.d != nil {
		pv.d.SetSelectedLines(editor.DiffLineRange{}, false)
	}
}

// patchBody shows the viewer, or why there is none. actions, when set,
// builds the buttons on the change under the pointer, and lets lines be
// selected.
func (a *App) patchBody(c *ui.Context, t *tab, pv *patchView, res session.PatchResult, actions func(c *ui.Context, change int)) {
	a.patchBodyWith(c, t, pv, res, actions, nil)
}

// patchBodyWith is patchBody with decorate, when set, given the viewer
// before it is built, to add what a review shows on its lines; it
// returns the viewer's element, or none while there is no viewer.
func (a *App) patchBodyWith(c *ui.Context, t *tab, pv *patchView, res session.PatchResult, actions func(c *ui.Context, change int),
	decorate func(d *editor.DiffState)) ui.Element {
	th := c.Theme()
	p := res.Parsed
	switch {
	case res.Err != nil:
		errorMessage(c, errText(res.Err))
		return ui.Element{}
	case p == nil:
		loading(c, "")
		return ui.Element{}
	case len(p.Files) == 0 || len(p.Hunks) == 0 && !p.Files[0].Binary:
		emptyState(c, locales.PatchNoDifferences())
		return ui.Element{}
	case pv.sub != nil && t != nil:
		a.submoduleChange(c, t, pv.sub)
		return ui.Element{}
	case p.Files[0].Binary && (pv.images[0] != nil || pv.images[1] != nil), pv.lfs && pv.pictured:
		pictures(c, pv)
		return ui.Element{}
	case pv.lfs:
		loading(c, "")
		return ui.Element{}
	case p.Files[0].Binary:
		emptyState(c, binarySizes(res))
		return ui.Element{}
	case pv.failed:
		emptyState(c, locales.PatchCannotShow())
		return ui.Element{}
	case pv.d == nil:
		loading(c, "")
		return ui.Element{}
	}
	if pv.endings != "" {
		// The engine names the change as "CRLF to LF" or "LF to CRLF".
		msg := locales.PatchEndingsToCRLF()
		if pv.endings == "CRLF to LF" {
			msg = locales.PatchEndingsToLF()
		}
		ui.Text(c, msg).FontSize(textSmall).TextColor(th.Warning).Padding(rowPadY, insetX)
	}
	d := pv.d
	a.setDiffView(d, false, actions)
	if wholeFile(p) {
		d.Layout = editor.DiffUnified
	}
	d.Annotations, d.RenderAnnotation, d.OnGutterButton = nil, nil, nil
	if decorate != nil {
		decorate(d)
	}
	el := editor.DiffView(c, d).Fill()
	switch {
	case !pv.shown:
		pv.shown = true
		if pv.scrollTo >= 0 {
			c.AnimationFrame()
		}
	case pv.scrollTo >= 0:
		d.SetScrollY(pv.scrollTo)
		pv.scrollTo = -1
	}
	return el
}

// setDiffView sets the viewer to the options chosen, the review's or
// the others', with actions, when set, on the change under the pointer
// and lines that can be selected.
func (a *App) setDiffView(d *editor.DiffState, review bool, actions func(c *ui.Context, change int)) {
	o := a.diffOpts()
	d.Theme = a.themes.code
	d.Layout = editor.DiffUnified
	if *o.layout(review) == 1 {
		d.Layout = editor.DiffSplit
	}
	d.Wrap = !o.noWrap
	d.LineSelection = actions != nil
	d.HunkActions = actions != nil
	d.RenderHunkActions = nil
	if actions != nil {
		d.RenderHunkActions = func(c *ui.Context, bar ui.Element, h *editor.DiffHunkActions) {
			bar.Row().Gap(spaceXS)
			i := h.Index
			bar.Children(func() { actions(c, i) })
		}
	}
}

// binarySizes says a binary file's patch is one, with the sizes of its
// sides when they were read.
func binarySizes(res session.PatchResult) string {
	old, new := len(res.Old), len(res.New)
	switch {
	case !res.HasSides || old == 0 && new == 0:
		return locales.PatchBinary()
	case old == 0:
		return locales.PatchBinarySize(byteSize(int64(new)))
	case new == 0:
		return locales.PatchBinarySize(byteSize(int64(old)))
	}
	return locales.PatchBinarySizes(byteSize(int64(old)), byteSize(int64(new)))
}

// wholeFile reports whether p adds or removes a file's every line, which
// split would show beside an empty half: it is shown unified.
func wholeFile(p *diff.Parsed) bool {
	return p != nil && len(p.Hunks) == 1 && (p.Hunks[0].OldCount == 0 || p.Hunks[0].NewCount == 0)
}

// isPicture reports whether a path names a picture the app can show.
func isPicture(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	}
	return false
}

// maxPixels bounds a picture decoded, against one that says it is huge.
const maxPixels = 64 << 20

// decodePicture decodes one side of a picture's change, and says its
// size; nil for a side that is none or does not decode.
func decodePicture(data []byte) (ui.ImageSource, string) {
	if len(data) == 0 {
		return nil, ""
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && cfg.Width*cfg.Height > maxPixels {
		return nil, byteSize(int64(len(data)))
	}
	img, err := ui.DecodeImage(data)
	if err != nil {
		return nil, byteSize(int64(len(data)))
	}
	w, h := ui.ImageSize(img)
	if b, ok := img.(*ui.Bitmap); ok && pictureScale(w, h) > 1 {
		// Shown larger, a small picture's pixels stay sharp squares, as
		// an icon's are meant to be seen.
		b.Pixelated()
	}
	return img, fmt.Sprintf("%d × %d, %s", int(w), int(h), byteSize(int64(len(data))))
}

// pictureScale is how many times its size a picture of w by h shows: a
// small one, as an icon, larger, by whole times, up to pictureShown.
func pictureScale(w, h float32) float32 {
	const pictureShown, most = 256, 8
	side := max(w, h)
	if side <= 0 || side*2 > pictureShown {
		return 1
	}
	return min(float32(int(pictureShown/side)), most)
}

// pictures shows a picture before and after its change, side by side.
func pictures(c *ui.Context, pv *patchView) {
	th := c.Theme()
	ui.Row(c).Fill().Gap(spaceXL).Padding(spaceXL).Children(func() {
		for k, title := range []string{locales.PatchBefore(), locales.PatchAfter()} {
			if pv.images[k] == nil && pv.about[k] == "" && !pv.notFetched[k] {
				continue
			}
			ui.Column(c.Key(k)).Grow(1).Basis(0).FillHeight().Gap(spaceM).Children(func() {
				ui.Row(c).Gap(spaceM).Children(func() {
					ui.Text(c, title).FontSize(textSmall).Bold()
					ui.Text(c, pv.about[k]).FontSize(textSmall).TextColor(th.TextMuted)
				})
				ui.Column(c).Grow(1).FillWidth().Center().Background(th.Surface).Radius(th.Radius).Padding(spaceM).Children(func() {
					if img := pv.images[k]; img != nil {
						w, h := ui.ImageSize(img)
						k := pictureScale(w, h)
						ui.Image(c, img).MaxWidth(w * k).MaxHeight(h * k).FillWidth().Label(title)
					} else if pv.notFetched[k] {
						ui.Text(c, locales.PatchNotFetched()).TextColor(th.TextMuted)
					} else {
						ui.Text(c, locales.PatchNotAPicture()).TextColor(th.TextMuted)
					}
				})
			})
		}
	})
}

// submoduleChange shows a submodule's change as a range of its commits,
// read from its repository in the work tree.
func (a *App) submoduleChange(c *ui.Context, t *tab, sc *subChange) {
	th := c.Theme()
	short := func(id []byte) string { return fmt.Sprintf("%.7x", id) }
	what := ""
	switch {
	case sc.old == nil:
		what = locales.PatchSubmoduleAdded(short(sc.new))
	case sc.new == nil:
		what = locales.PatchSubmoduleRemoved(short(sc.old))
	case bytes.Equal(sc.old, sc.new):
		what = locales.PatchSubmoduleAt(short(sc.new))
	default:
		what = locales.PatchSubmoduleMoves(short(sc.old), short(sc.new))
	}
	if sc.old != nil && sc.new != nil && !bytes.Equal(sc.old, sc.new) && !sc.asked {
		sc.asked = true
		t.s.LoadSubmoduleRange(sc.path, sc.old, sc.new, func(r session.SubmoduleRange) { sc.rng = &r })
	}
	type row struct {
		c       session.RangeCommit
		removed bool
	}
	var rows []row
	if r := sc.rng; r != nil {
		for _, rc := range r.Added {
			rows = append(rows, row{rc, false})
		}
		for _, rc := range r.Removed {
			rows = append(rows, row{rc, true})
		}
	}
	ui.Column(c).Fill().Gap(spaceM).Padding(spaceL).Children(func() {
		ui.Text(c, what).Bold().Selectable()
		if sc.dirty {
			ui.Text(c, locales.PatchSubmoduleDirty()).FontSize(textSmall).TextColor(th.Warning)
		}
		r := sc.rng
		switch {
		case sc.asked && r == nil:
			ui.Spinner(c).Label(locales.CommonLoading())
			return
		case r == nil:
			return
		case r.Err != nil:
			ui.Text(c, errText(r.Err)).FontSize(textSmall).TextColor(th.TextMuted).Selectable()
			return
		}
		added, removed := len(r.Added), len(r.Removed)
		var summary string
		switch {
		case removed > 0 && r.More:
			summary = locales.PatchSubmoduleSummaryRemovedMore(added, removed)
		case removed > 0:
			summary = locales.PatchSubmoduleSummaryRemoved(added, removed)
		case r.More:
			summary = locales.PatchSubmoduleSummaryMore(added)
		default:
			summary = locales.PatchSubmoduleSummary(added)
		}
		ui.Text(c, summary).FontSize(textSmall).TextColor(th.TextMuted)
		sc.list.Label = func(i int) string {
			subject := redact.Control(rows[i].c.Subject)
			if rows[i].removed {
				return locales.PatchSubmoduleCommitRemoved(subject)
			}
			return locales.PatchSubmoduleCommitAdded(subject)
		}
		ui.List(c, &sc.list, len(rows), func(i int) {
			rc := rows[i]
			ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).FillWidth().Children(func() {
				sign, col := "+", th.Success
				if rc.removed {
					sign, col = "-", th.Danger
				}
				ui.Text(c, sign).Font("monospace").FontSize(textSmall).TextColor(col).Width(12)
				ui.Text(c, rc.c.ID[:min(10, len(rc.c.ID))]).Font("monospace").FontSize(textSmall).TextColor(th.TextMuted)
				ui.Text(c, redact.Control(rc.c.Subject)).SingleLine().Grow(1)
			})
		}).Label(locales.PatchSubmoduleCommits()).Grow(1)
	})
}

// fileLine is the line of the file, from 1, that line i of the viewer's
// side shows; 0 for none, as a hunk's header or a side's padding.
func (pv *patchView) fileLine(side editor.DiffSide, i int) int {
	if i < 0 {
		return 0
	}
	if pv.whole {
		return i + 1
	}
	at := pv.newAt
	if side == editor.DiffOld {
		at = pv.oldAt
	}
	if pv.p == nil || i >= len(at) || at[i] < 0 || int(at[i]) >= len(pv.p.Lines) {
		return 0
	}
	l := pv.p.Lines[at[i]]
	if side == editor.DiffOld {
		return int(l.Old)
	}
	return int(l.New)
}

// across is the line of the other side that line n of the file, from
// 1, on side faces when it is unchanged in the patch; 0 for a line added
// or removed, or none of the patch's.
func (pv *patchView) across(side editor.DiffSide, n int) int {
	if pv.p == nil || n <= 0 {
		return 0
	}
	for _, l := range pv.p.Lines {
		at, other := l.New, l.Old
		if side == editor.DiffOld {
			at, other = l.Old, l.New
		}
		if int(at) == n {
			if l.Kind != ' ' {
				return 0
			}
			return int(other)
		}
	}
	return 0
}

// viewLine is the line of the viewer's side that shows line n of the
// file, from 1; -1 when it shows none.
func (pv *patchView) viewLine(side editor.DiffSide, n int) int {
	if n <= 0 {
		return -1
	}
	if pv.whole {
		return n - 1
	}
	at := pv.newAt
	if side == editor.DiffOld {
		at = pv.oldAt
	}
	for i := range at {
		if pv.fileLine(side, i) == n {
			return i
		}
	}
	return -1
}

// inHunk reports whether line n of the file, from 1, on side is among
// the patch's hunks, context lines included: where a host takes a
// comment on a pull request's diff.
func (pv *patchView) inHunk(side editor.DiffSide, n int) bool {
	if pv.p == nil {
		return false
	}
	for _, h := range pv.p.Hunks {
		start, count := h.NewStart, h.NewCount
		if side == editor.DiffOld {
			start, count = h.OldStart, h.OldCount
		}
		if int32(n) >= start && int32(n) < start+count {
			return true
		}
	}
	return false
}
