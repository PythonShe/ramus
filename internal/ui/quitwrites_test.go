package ui

import (
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ramus/internal/issuelink"
	"ramus/internal/library"
)

// A branch pinned, a ref hidden and an issue-link rule saved just before
// a quit are in the library's file once WaitWrites, which the quit calls,
// returns.
func TestQuitWaitsForRefsAndLinks(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(h.closeAll)
	r := h.repoWith()
	r.Git("branch", "side")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.app.toggleRef(tb, &tb.prefs.pinned, "refs/heads/side")
	h.app.toggleRef(tb, &tb.prefs.hidden, "refs/heads/main")
	h.app.openLinks(tb)
	h.tt.Frame()
	h.app.links.rules[0] = issuelink.Rule{Pattern: `\bPROJ-(\d+)\b`, URL: "https://jira.example.com/browse/PROJ-$1"}
	h.app.saveLinks()
	if !h.app.WaitWrites(10 * time.Second) {
		t.Fatal("the library was not written in time")
	}
	back, err := library.Load(h.lib.Path())
	if err != nil {
		t.Fatal(err)
	}
	pinned, hidden := back.RefPrefs(r.Dir)
	if !slices.Contains(pinned, "refs/heads/side") || !slices.Contains(hidden, "refs/heads/main") {
		t.Fatalf("library file has pinned %q, hidden %q", pinned, hidden)
	}
	if got := back.IssueLinks(r.Dir); len(got) != 1 {
		t.Fatalf("library file has the rules %+v", got)
	}
}

// blockMain has the app's updates wait, as they do on a main thread
// held by a quit, until the returned func lets them through.
func blockMain(h *harness) (release func()) {
	held := make(chan struct{})
	updates := h.updates
	h.app.Update = func(fn func()) {
		<-held
		updates <- fn
	}
	var once sync.Once
	release = func() { once.Do(func() { close(held) }) }
	h.t.Cleanup(release)
	return release
}

// A change of the library being written as the app quits counts as
// written once it is on disk: the quit, which holds the main thread,
// does not wait for the result to be shown there.
func TestQuitWaitsNotOnALibraryNotice(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(h.closeAll)
	r := h.repoWith()
	if err := h.lib.Opened(r.Dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	release := blockMain(h)
	h.app.libraryWrite(func(l *library.Library) error { return l.SetFavourite(r.Dir, true) })
	if !h.app.WaitWrites(10 * time.Second) {
		t.Fatal("the write waited for the main thread")
	}
	back, err := library.Load(h.lib.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Favourites(); len(got) != 1 {
		t.Fatalf("library file has the favourites %+v", got)
	}
	release()
	h.until("the manager told", func() bool { return h.app.manager.dirty })
}

// An identity write that fails as the app quits leaves the saver idle
// before its failure is told: the quit's wait for it does not wait for
// the main thread, and the failure is said once that runs.
func TestQuitWaitsNotOnAnIdentityFailure(t *testing.T) {
	h, _ := identityHarness(t)
	h.tt.Frame()
	h.app.openSettings()
	s := &h.app.settingsUI
	h.until("the identity", func() bool { return s.identRead })
	s.ident.DefaultBranch = "two words"
	release := blockMain(h)
	h.app.SaveIdentity()
	waited := make(chan struct{})
	go func() {
		h.app.WaitIdentity(time.Minute)
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the wait for the identity waited for the main thread")
	}
	release()
	h.until("the refusal", func() bool { return strings.Contains(s.err, "two words") })
}

// A quit's wait for a write that does not end returns at its bound and
// leaves nothing waiting behind it, however many times it is made: the
// files' writes and the identity's.
func TestQuitWaitsLeaveNothingBehind(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	h.app.goWrite(func() func() { <-release; return nil })
	is := newIdentSaver()
	is.running, is.idle = true, make(chan struct{}) // a write that hangs
	h.app.settingsUI.identSaver = is
	before := runtime.NumGoroutine()
	const waits = 50
	for range waits {
		if h.app.WaitWrites(time.Millisecond) {
			t.Fatal("a write that hangs was waited out")
		}
		if h.app.WaitIdentity(time.Millisecond) {
			t.Fatal("an identity that hangs was waited out")
		}
	}
	// Other work of the app may come and go meanwhile, not one goroutine
	// a wait.
	if n := runtime.NumGoroutine() - before; n >= waits {
		t.Fatalf("%d goroutines more after %d waits", n, 2*waits)
	}
	close(release)
	is.mu.Lock()
	is.running = false
	close(is.idle)
	is.mu.Unlock()
	if !h.app.WaitWrites(10*time.Second) || !h.app.WaitIdentity(10*time.Second) {
		t.Fatal("the writes ended, the waits did not")
	}
}
