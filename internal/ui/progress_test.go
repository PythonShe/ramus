package ui

import (
	"context"
	"errors"
	"testing"

	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// A write that prints and then hangs shows in the status bar with what
// it printed last, and Cancel there stops it.
func TestRunningWriteCancel(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	op := write.Op{Name: "Slow fetch", Cmd: run.Cmd{GitDir: r.GitDir(), WorkTree: r.Dir,
		Config: []string{"alias.slow=!echo 'Receiving objects:  42% (42/100)' >&2; sleep 30"}, Args: []string{"slow"}}}
	if _, err := tb.s.Submit(op); err != nil {
		t.Fatal(err)
	}
	h.until("the progress", h.shows("Slow fetch: Receiving objects:  42% (42/100)"))
	h.click("Cancel Slow fetch")
	h.until("cancelled", func() bool {
		e := tb.s.Log[len(tb.s.Log)-1]
		return !e.Running && errors.Is(e.Err, context.Canceled) && !h.tt.HasText("Slow fetch: Receiving objects:  42% (42/100)")
	})
}
