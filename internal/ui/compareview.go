package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/internal/git/redact"
	"ramus/internal/session"
)

// maxCompareSide bounds a file read whole to unfold the lines around the
// changes of a comparison.
const maxCompareSide = 4 << 20

// compareState is a comparison of two files or folders outside any
// repository, as ramus diff asks for.
type compareState struct {
	open     bool
	old, new string
	// dir is set once both sides are known to be folders.
	dir     bool
	loading bool
	// pending waits for git to be found.
	pending bool
	err     string
	list    *diff.Compared
	rows    ui.ListState
	chosen  int
	split   float32
	key     compareKey
	patch   session.PatchResult
	pv      patchView
	cancel  context.CancelFunc
	// closed runs once the comparison is closed, for a tool that waits.
	closed func()
}

// compareKey is the patch shown, to read it again when it changes.
type compareKey struct {
	old, new string
	ws       diff.Whitespace
}

// Compare shows what differs between old and new, absolute paths to two
// files or two folders. closed, when set, runs once it is closed.
func (a *App) Compare(old, new string, closed func()) {
	a.stopCompare()
	a.compareUI = compareState{open: true, old: old, new: new, chosen: -1, split: 320, loading: true, closed: closed}
	if a.Runner == nil {
		a.compareUI.pending = true
		return
	}
	a.startCompare()
}

func (a *App) startCompare() {
	cs := &a.compareUI
	cs.pending = false
	ctx, cancel := context.WithCancel(context.Background())
	cs.cancel = cancel
	old, new, r, update := cs.old, cs.new, a.Runner, a.Update
	go func() {
		oi, err1 := os.Stat(old)
		ni, err2 := os.Stat(new)
		var list *diff.Compared
		var err error
		switch {
		case err1 != nil:
			err = err1
		case err2 != nil:
			err = err2
		case oi.IsDir() != ni.IsDir():
			err = fmt.Errorf("%s and %s are not both files or both folders", filepath.Base(old), filepath.Base(new))
		case oi.IsDir():
			list, err = diff.CompareFolders(ctx, r, old, new, 0)
		}
		update(func() {
			if ctx.Err() != nil {
				return
			}
			cs.loading, cs.list = false, list
			cs.dir = list != nil
			if err != nil {
				cs.err = errText(err)
			}
			if list != nil && list.Len() > 0 {
				cs.chosen = 0
			}
		})
	}()
}

func (a *App) stopCompare() {
	cs := &a.compareUI
	if cs.cancel != nil {
		cs.cancel()
		cs.cancel = nil
	}
	if cs.closed != nil {
		done := cs.closed
		cs.closed = nil
		done()
	}
}

// comparedFiles is the pair of files the comparison shows now, "" for a
// side that does not exist.
func (cs *compareState) comparedFiles() (string, string, bool) {
	if !cs.dir {
		return cs.old, cs.new, cs.err == "" && !cs.loading
	}
	if cs.list == nil || cs.chosen < 0 || cs.chosen >= cs.list.Len() {
		return "", "", false
	}
	side := func(root, rel string) string {
		if rel == "" {
			return ""
		}
		return filepath.Join(root, filepath.FromSlash(rel))
	}
	return side(cs.old, cs.list.Old(cs.chosen)), side(cs.new, cs.list.New(cs.chosen)), true
}

func (a *App) loadComparePatch(cs *compareState) {
	old, new, ok := cs.comparedFiles()
	key := compareKey{old, new, diff.Whitespace(a.diffOpts().ws)}
	if !ok || cs.key == key || a.Runner == nil {
		return
	}
	cs.key, cs.patch = key, session.PatchResult{}
	r, update := a.Runner, a.Update
	go func() {
		res := session.PatchResult{}
		p, err := diff.ComparePatch(context.Background(), r, key.old, key.new, key.ws, MaxShownPatch)
		res.Truncated, res.Err = p.Truncated, err
		if err == nil {
			res.Parsed, res.Err = diff.Parse(p.Text)
		}
		if res.Err == nil && !p.Truncated {
			o, ok1 := readSide(key.old)
			n, ok2 := readSide(key.new)
			res.Old, res.New, res.HasSides = o, n, ok1 && ok2
		}
		update(func() {
			if cs.key == key {
				cs.patch = res
				a.setPatch(&cs.pv, res)
			}
		})
	}()
}

// readSide is a file read whole, when it is small enough to; none for "".
func readSide(path string) ([]byte, bool) {
	if path == "" {
		return nil, true
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCompareSide+1))
	return data, err == nil && len(data) <= maxCompareSide
}

func (a *App) compareView(c *ui.Context) {
	cs := &a.compareUI
	if !cs.open {
		if cs.cancel != nil || cs.closed != nil {
			a.stopCompare()
		}
		return
	}
	th := c.Theme()
	a.loadComparePatch(cs)
	ui.Modal(c, &cs.open, func() {
		ui.Column(c).Width(1100).Height(760).Gap(10).Padding(20, 0).Children(func() {
			ui.Column(c).Padding(0, 20).Gap(2).Children(func() {
				ui.Text(c, "Compare").Bold()
				for _, p := range []string{cs.old, cs.new} {
					ui.Text(c, redact.Control(p)).Font("monospace").FontSize(12).TextColor(th.TextMuted).SingleLine().Selectable()
				}
			})
			a.diffBar(c, func() {
				if ui.PrimaryButton(c, "Close").Clicked() {
					cs.open = false
				}
			})
			switch {
			case cs.err != "":
				ui.Text(c, cs.err).TextColor(th.Danger).Padding(12, 20).Selectable()
			case cs.loading:
				ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			case cs.dir:
				ui.Split(c, &cs.split, func() { a.comparedList(c, cs) }, func() { a.comparedPatch(c, cs) }).SplitReset(320).Fill()
			default:
				a.comparedPatch(c, cs)
			}
		})
	})
}

func (a *App) comparedList(c *ui.Context, cs *compareState) {
	th := c.Theme()
	l := cs.list
	ui.Column(c).Fill().Children(func() {
		if l.Len() == 0 {
			ui.Column(c).Fill().Center().Children(func() {
				ui.Text(c, "No differences").TextColor(th.TextMuted)
			})
			return
		}
		count := countFiles(l.Len()) + " differ"
		if l.Truncated {
			count = "The first " + count
		}
		ui.Text(c, count).FontSize(12).TextColor(th.TextMuted).Padding(4, 12)
		name := func(i int) string {
			switch {
			case l.Old(i) == "":
				return l.New(i)
			case l.New(i) == "" || l.Old(i) == l.New(i):
				return l.Old(i)
			}
			return l.Old(i) + " → " + l.New(i)
		}
		cs.rows.Selected = &cs.chosen
		cs.rows.Label = func(i int) string { return fileStatusWord(l.Status(i)) + " " + name(i) }
		ui.List(c, &cs.rows, l.Len(), func(i int) {
			ui.Row(c).Padding(2, 12).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Text(c, string(l.Status(i))).Font("monospace").FontSize(12).Width(14).TextColor(fileStatusColor(th, l.Status(i)))
				shown, why := showPath(name(i))
				txt := ui.Text(c, shown).FontSize(13).SingleLine().Grow(1)
				if why != "" {
					txt.TextColor(th.Warning).Tooltip(why)
				}
			})
		}).Grow(1)
	})
}

func (a *App) comparedPatch(c *ui.Context, cs *compareState) {
	th := c.Theme()
	if _, _, ok := cs.comparedFiles(); !ok {
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "Choose a file to see its changes").TextColor(th.TextMuted)
		})
		return
	}
	ui.Column(c).Fill().Children(func() {
		if cs.patch.Truncated {
			ui.Text(c, fmt.Sprintf("Only the first %d MB of this patch are shown", MaxShownPatch>>20)).
				FontSize(12).TextColor(th.Warning).Padding(4, 12)
		}
		a.patchBody(c, nil, &cs.pv, cs.patch, nil)
	})
}
