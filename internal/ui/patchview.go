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
	"ramus/internal/session"
	"ramus/internal/settings"
)

// diffOptions are how every patch shows: the layout and wrapping are
// kept in the settings, white space for the session.
type diffOptions struct {
	split int // 0 unified, 1 side by side
	wrap  bool
	ws    int // a diff.Whitespace
}

func (a *App) diffOpts() *diffOptions {
	o := &a.diff
	if !o.loaded {
		o.loaded = true
		if a.Settings.DiffSplit {
			o.split = 1
		}
		o.wrap = a.Settings.DiffWrap
	}
	return &o.diffOptions
}

// diffBar is the options of the patch shown: layout, wrapping and white
// space. A change of layout or wrapping is saved in the settings.
func (a *App) diffBar(c *ui.Context) {
	o := a.diffOpts()
	ws := []string{"All White Space", "Ignore Space Changes", "Ignore All Space"}
	ui.PopoverMenuButton(c, ws[o.ws], func(m *ui.Menu) {
		for i, label := range ws {
			if m.Item(label).Checked(o.ws == i).Chosen() {
				o.ws = i
			}
		}
	})
	wrap := ui.Checkbox(c, &o.wrap, "Wrap")
	layout := ui.Segmented(c, &o.split, "Unified", "Split")
	if wrap.Changed() || layout.Changed() {
		a.Settings.DiffSplit, a.Settings.DiffWrap = o.split == 1, o.wrap
		if a.SettingsPath != "" {
			path, saved, update := a.SettingsPath, a.Settings, a.Update
			go func() {
				if err := settings.Save(path, saved); err != nil {
					update(func() { a.notice = "Settings not saved: " + errText(err) })
				}
			}()
		}
	}
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
}

// setPatch builds the viewer of res off the main thread: with the whole
// files when res has them and the patch makes one from the other, so the
// lines around its hunks unfold, else of the patch alone.
func (a *App) setPatch(pv *patchView, res session.PatchResult) {
	pv.gen++
	gen, update := pv.gen, a.Update
	scroll := -1.0
	if pv.d != nil && pv.p != nil && res.Parsed != nil && len(pv.p.Files) > 0 && len(res.Parsed.Files) > 0 &&
		pv.p.Path(0) == res.Parsed.Path(0) {
		scroll = pv.d.ScrollY()
	}
	pv.d, pv.p, pv.failed, pv.images, pv.about = nil, res.Parsed, false, [2]ui.ImageSource{}, [2]string{}
	p := res.Parsed
	if p == nil || len(p.Files) == 0 {
		return
	}
	if p.Files[0].Binary {
		if res.HasSides && isPicture(p.Path(0)) {
			go func() {
				var imgs [2]ui.ImageSource
				var about [2]string
				for k, data := range [][]byte{res.Old, res.New} {
					imgs[k], about[k] = decodePicture(data)
				}
				update(func() {
					if pv.gen == gen {
						pv.images, pv.about = imgs, about
					}
				})
			}()
		}
		return
	}
	go func() {
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
		if res.HasSides {
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
	}()
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
func (a *App) patchBody(c *ui.Context, pv *patchView, res session.PatchResult, actions func(c *ui.Context, change int)) {
	th := c.Theme()
	p := res.Parsed
	switch {
	case res.Err != nil:
		ui.Text(c, errText(res.Err)).TextColor(th.Danger).Padding(12).Selectable()
		return
	case p == nil:
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	case len(p.Files) == 0 || len(p.Hunks) == 0 && !p.Files[0].Binary:
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "No differences in content").TextColor(th.TextMuted)
		})
		return
	case p.Files[0].Binary && (pv.images[0] != nil || pv.images[1] != nil):
		pictures(c, pv)
		return
	case p.Files[0].Binary:
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "Binary file").TextColor(th.TextMuted)
		})
		return
	case pv.failed:
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "This patch cannot be shown").TextColor(th.TextMuted)
		})
		return
	case pv.d == nil:
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	}
	if pv.endings != "" {
		ui.Text(c, "Line endings change from "+pv.endings+" on every line shown").
			FontSize(12).TextColor(th.Warning).Padding(4, 12)
	}
	o := a.diffOpts()
	d := pv.d
	d.Layout = editor.DiffUnified
	if o.split == 1 {
		d.Layout = editor.DiffSplit
	}
	d.Wrap = o.wrap
	d.LineSelection = actions != nil
	d.HunkActions = actions != nil
	d.RenderHunkActions = nil
	if actions != nil {
		d.RenderHunkActions = func(c *ui.Context, bar ui.Element, h *editor.DiffHunkActions) {
			bar.Row().Gap(4)
			i := h.Index
			bar.Children(func() { actions(c, i) })
		}
	}
	editor.DiffView(c, d).Fill()
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
	return img, fmt.Sprintf("%d × %d, %s", int(w), int(h), byteSize(int64(len(data))))
}

// pictures shows a picture before and after its change, side by side.
func pictures(c *ui.Context, pv *patchView) {
	th := c.Theme()
	ui.Row(c).Fill().Gap(16).Padding(16).Children(func() {
		for k, title := range []string{"Before", "After"} {
			if pv.images[k] == nil && pv.about[k] == "" {
				continue
			}
			ui.Column(c).Key(k).Grow(1).Basis(0).FillHeight().Gap(8).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, title).FontSize(12).Bold()
					ui.Text(c, pv.about[k]).FontSize(12).TextColor(th.TextMuted)
				})
				ui.Column(c).Grow(1).FillWidth().Center().Background(th.Surface).Radius(6).Padding(8).Children(func() {
					if img := pv.images[k]; img != nil {
						w, h := ui.ImageSize(img)
						ui.Image(c, img).MaxWidth(w).MaxHeight(h).FillWidth().Label(title)
					} else {
						ui.Text(c, "Not a picture that can be shown").TextColor(th.TextMuted)
					}
				})
			})
		}
	})
}
