package ui

import (
	"slices"
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
	// Two saves of the refs at once may land in either order, as each
	// runs on a goroutine of its own: one at a time here.
	h.app.WaitWrites(10 * time.Second)
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
