package ui

import (
	"slices"
	"testing"
	"time"

	"ramus/internal/issuelink"
	"ramus/internal/library"
)

// A pinned branch and a repository's issue links are written to the
// library before a quit lets the process end, as its other writes are:
// one made just before would be lost, and a test's folder removed under
// the write.
func TestRefPrefsAndLinksWrittenBeforeQuit(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()

	h.app.toggleRef(tb, &tb.prefs.pinned, "refs/heads/main")
	h.app.openLinks(tb)
	h.app.links.rules[0] = issuelink.Rule{Pattern: `\bPROJ-(\d+)\b`, URL: "https://jira.example.com/browse/PROJ-$1"}
	h.app.saveLinks()
	if !h.app.WaitWrites(10 * time.Second) {
		t.Fatal("the writes did not end")
	}
	lib, err := library.Load(h.lib.Path())
	if err != nil {
		t.Fatal(err)
	}
	if pinned, _ := lib.RefPrefs(tb.s.Path); !slices.Equal(pinned, []string{"refs/heads/main"}) {
		t.Errorf("pinned %q once the writes were waited for", pinned)
	}
	if links := lib.IssueLinks(tb.s.Path); len(links) != 1 {
		t.Errorf("issue links %v once the writes were waited for", links)
	}
}
