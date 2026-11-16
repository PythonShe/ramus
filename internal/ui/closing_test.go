package ui

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// hang submits a write of tab tb's repository that runs until stopped.
func (h *harness) hang(tb *tab) {
	h.t.Helper()
	op := write.Op{Name: "Slow push", Cmd: run.Cmd{GitDir: tb.s.Repo.GitDir, WorkTree: tb.s.Repo.WorkTree,
		Config: []string{"alias.slow=!sleep 30"}, Args: []string{"slow"}}}
	if _, err := tb.s.Submit(op); err != nil {
		h.t.Fatal(err)
	}
}

// Closing a tab while a write runs there asks first, and the write is
// not stopped until the user says so.
func TestCloseTabWhileWriting(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.hang(tb)
	h.tt.Key(ui.Cmd, ui.KeyW)
	h.until("the question", h.shows("Slow push is still running in "+h.app.title(tb)))
	h.click("Cancel")
	if len(h.app.tabs) != 1 || busyWrite(tb) == nil {
		t.Fatalf("closed %d tabs, or stopped the write", 1-len(h.app.tabs))
	}
	h.tt.Key(ui.Cmd, ui.KeyW)
	h.until("the question again", h.shows("Stop and Close"))
	h.click("Stop and Close")
	h.until("closed", func() bool { return len(h.app.tabs) == 0 })
}

// Closing the window or quitting while a write runs asks first; once
// told to, the writes stop and the window may go.
func TestQuitWhileWriting(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	if h.app.AskToClose(true, func() { t.Fatal("asked with nothing running") }) {
		t.Fatal("asked with nothing running")
	}
	tb := h.app.current()
	h.hang(tb)
	h.until("running", h.app.Writing)
	quit := false
	if !h.app.AskToClose(true, func() { quit = true }) {
		t.Fatal("did not ask")
	}
	h.until("the question", h.shows("Quit Ramus?"))
	h.click("Stop and Quit")
	if !quit {
		t.Fatal("not told to quit")
	}
	h.app.StopWrites()
	h.until("stopped", func() bool { return !h.app.Writing() })
	if e := tb.s.Log[len(tb.s.Log)-1]; !errors.Is(e.Err, context.Canceled) {
		t.Fatalf("the write ended with %v", e.Err)
	}
}

// Quitting while a clone runs asks first too.
func TestQuitWhileCloning(t *testing.T) {
	h := newHarness(t)
	h.app.clone = cloneState{open: true, url: "https://example.com/o/r.git", starting: true}
	if !h.app.AskToClose(true, func() {}) {
		t.Fatal("did not ask")
	}
	h.until("the question", h.shows("The clone of https://example.com/o/r.git is still running, and stops part way."))
}
