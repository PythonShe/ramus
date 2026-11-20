package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// hang submits a write of tab tb's repository that runs until stopped.
func (h *harness) hang(tb *tab) {
	h.t.Helper()
	op := write.Op{Name: "Slow push", Cmd: run.Cmd{GitDir: tb.s.Repo.GitDir, WorkTree: tb.s.Repo.WorkTree,
		Config: []string{"alias.slow=!sleep 600"}, Args: []string{"slow"}}}
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
	h.until("the repository", h.shows("No local changes"))
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

// The question before quitting during a clone names its URL without the
// credentials typed in it.
func TestQuitWhileCloningHidesCredentials(t *testing.T) {
	h := newHarness(t)
	h.app.clone.url = "https://me:s3cret-token@git.example/r.git"
	h.app.clone.starting = true
	if !h.app.AskToClose(true, func() {}) {
		t.Fatal("did not ask")
	}
	if m := h.app.confirm.message; strings.Contains(m, "s3cret-token") || !strings.Contains(m, "git.example/r.git") {
		t.Fatalf("asked %q", m)
	}
	h.app.clone.starting = false
}

// Reopening a repository, as the warning bar's Reopen and the panic
// banner offer, closes its tab: with a write running it asks first, as
// closing the tab does.
func TestReopenWhileWriting(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	tb.sel = "history"
	h.hang(tb)
	h.until("running", h.app.Writing)
	h.app.reopen(tb)
	h.until("the question", h.shows("Slow push is still running in "+h.app.title(tb)))
	h.click("Cancel")
	if h.app.current() != tb || busyWrite(tb) == nil {
		t.Fatal("reopened, or stopped the write, without asking")
	}
	h.app.reopen(tb)
	h.until("the question again", h.shows("Stop and Close"))
	h.click("Stop and Close")
	h.until("reopened", func() bool { return len(h.app.tabs) == 1 && h.app.current() != tb })
	if h.app.current().sel != "history" {
		t.Fatalf("reopened on %q", h.app.current().sel)
	}
}

// Closing the window or quitting while a write runs asks first; once
// told to, the writes stop and the window may go.
func TestQuitWhileWriting(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
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
