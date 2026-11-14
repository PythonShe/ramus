package ui

import (
	"encoding/hex"
	"strings"
	"testing"

	"ramus/internal/layout"
	"ramus/internal/library"
)

// What a window has open is told when it changes, and opens again with
// each tab on its section and its commit and file chosen: before git is
// found too, with the tab that was shown in front.
func TestLayoutRestore(t *testing.T) {
	h := newHarness(t)
	one, two := h.repoWith(), h.repoWith()
	one.Commit("two")
	first := strings.TrimSpace(one.Git("rev-parse", "HEAD~1"))
	one.Write("b.txt", "b")
	one.Write("a.txt", "a")
	changed := 0
	h.app.LayoutChanged = func() { changed++ }
	h.app.Open(one.Dir)
	h.app.Open(two.Dir)
	h.until("both tabs", func() bool { return len(h.app.tabs) == 2 && h.tt.HasText("No changes") })

	// The first tab: its history, an older commit, a changed file.
	h.app.active = 0
	tb := h.app.current()
	h.until("its changes", func() bool { return tb.cv.model != nil && len(tb.cv.model.rows) > 0 })
	for i, r := range tb.cv.model.rows {
		if r.kind == rowFile && string(tb.cv.model.st.Path(int(r.idx))) == "b.txt" {
			tb.cv.chosen = i
		}
	}
	id, _ := hex.DecodeString(first)
	h.app.chooseCommit(tb, id)
	tb.sel = "history"
	before := changed
	h.tt.Frame()
	if changed == before {
		t.Fatal("not told of a change")
	}
	l := h.app.Layout()
	want := layout.Window{Active: 0, Tabs: []layout.Tab{
		{Path: library.Clean(one.Dir), Section: "history", Commit: first, File: "b.txt"},
		{Path: library.Clean(two.Dir)},
	}}
	if !layoutEqual(l, want) {
		t.Fatalf("layout %+v, want %+v", l, want)
	}
	before = changed
	h.tt.Frame()
	if changed != before {
		t.Fatal("told with nothing changed")
	}

	// Opened again, git not found yet.
	h.closeAll()
	for len(h.app.tabs) > 0 {
		h.app.Close(0)
	}
	runner := h.app.Runner
	h.app.Runner = nil
	l.Active = 0
	h.app.Restore(l)
	if len(h.app.tabs) != 0 {
		t.Fatal("tabs before git")
	}
	h.app.SetRunner(runner, nil)
	if len(h.app.tabs) != 2 || h.app.active != 0 {
		t.Fatalf("%d tabs, %d shown", len(h.app.tabs), h.app.active)
	}
	tb = h.app.current()
	if tb.sel != "history" {
		t.Fatalf("section %q", tb.sel)
	}
	h.until("the commit chosen", func() bool { return tb.hist.chosen >= 0 && hex.EncodeToString(tb.hist.chosenID) == first })
	tb.sel = "changes"
	h.until("the file chosen", func() bool {
		r, ok := tb.cv.chosenFile()
		return ok && string(tb.cv.model.st.Path(int(r.idx))) == "b.txt"
	})
	if h.app.tabs[1].sel != "changes" {
		t.Fatalf("second tab on %q", h.app.tabs[1].sel)
	}
}
