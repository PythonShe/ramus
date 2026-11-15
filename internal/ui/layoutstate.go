package ui

import (
	"encoding/hex"
	"slices"

	"ramus/internal/layout"
	"ramus/internal/library"
)

// restorable are the sections a tab reopens on; the others (a comparison,
// a worktree or submodule being opened) show changes again.
var restorable = []string{"changes", "review", "history", "reflog", "refs", "stats", "log", "snapshots", "stashes", "worktrees"}

// Layout is what this window has open, to open again at the next launch.
func (a *App) Layout() layout.Window {
	w := layout.Window{Active: a.active}
	for _, t := range a.tabs {
		lt := layout.Tab{Path: t.s.Path}
		if name := viewName(t); name != "changes" && slices.Contains(restorable, name) {
			lt.Section = name
		}
		if id := t.hist.chosenID; id != nil {
			lt.Commit = hex.EncodeToString(id)
		} else if t.hist.want != nil {
			lt.Commit = hex.EncodeToString(t.hist.want) // not found yet
		}
		if r, ok := t.cv.chosenFile(); ok {
			lt.File = string(t.cv.model.st.Path(int(r.idx)))
		} else {
			lt.File = t.cv.want
		}
		w.Tabs = append(w.Tabs, lt)
	}
	return w
}

// Restore opens what a window had open: each tab on its section, with
// its commit and file chosen once they are read. Before git is found the
// tabs wait with the other repositories asked for.
func (a *App) Restore(w layout.Window) {
	if a.restoring == nil {
		a.restoring = map[string]restoredTab{}
	}
	for i, lt := range w.Tabs {
		a.restoring[library.Clean(lt.Path)] = restoredTab{lt, i == w.Active}
	}
	for _, lt := range w.Tabs {
		a.Open(lt.Path)
	}
	if a.Runner != nil {
		a.restoring = nil
	}
}

// restoredTab is a tab as it was when the app last ran.
type restoredTab struct {
	layout.Tab
	active bool
}

func (r restoredTab) apply(t *tab) {
	switch {
	case r.Section == "review": // a mode of the changes view
		t.sel, t.cv.mode = "changes", modeReview
	case slices.Contains(restorable, r.Section):
		t.sel = r.Section
	}
	if id, err := hex.DecodeString(r.Commit); err == nil && len(id) > 0 {
		t.hist.want, t.hist.wantFor = id, nil
	}
	t.cv.want = r.File
}

// noteLayout tells LayoutChanged when what is open changed since the
// last frame.
func (a *App) noteLayout() {
	if a.LayoutChanged == nil {
		return
	}
	l := a.Layout()
	if layoutEqual(l, a.lastLayout) {
		return
	}
	a.lastLayout = l
	a.LayoutChanged()
}

func layoutEqual(x, y layout.Window) bool {
	return x.Active == y.Active && slices.Equal(x.Tabs, y.Tabs)
}
