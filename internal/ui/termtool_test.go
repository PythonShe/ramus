package ui

import (
	"testing"

	"ramus/locales"
)

// A merge tool that runs in a terminal is refused before it starts, on
// every system: the app has no terminal to show it in, and it would hold
// the queue waiting unseen.
func TestTerminalMergeToolRefused(t *testing.T) {
	h := newHarness(t)
	r := conflictingRepo(h)
	r.Git("config", "merge.tool", "vimdiff")
	r.Run(nil, "merge", "topic")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the bar", h.shows("Merge stopped"))
	tb := h.app.current()
	tb.sel = "changes"
	h.tt.Frame()
	h.until("the conflicts", func() bool { return hasExact(h, "a.txt") })
	n := len(tb.s.Log)
	h.menu("a.txt", locales.MergeOpenInTool())
	if want := locales.MergeTerminalTool("vimdiff"); h.app.notice != want {
		t.Fatalf("notice %q, want %q", h.app.notice, want)
	}
	if len(tb.s.Log) != n {
		t.Fatalf("ran %s", tb.s.Log[len(tb.s.Log)-1].Name)
	}
}
