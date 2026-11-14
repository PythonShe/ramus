package ui

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The filter bar narrows history to what git would list; a hidden branch
// stays out of history and stays hidden once the repository opens again,
// and a pinned branch lists first.
func TestHistoryFilter(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("switch", "-q", "-c", "side")
	r.Write("s.txt", "needle\n")
	r.Commit("side needle")
	r.Git("switch", "-q", "main")
	r.Write("m.txt", "main\n")
	r.Commit("main work")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	r.Git("switch", "-q", "side")
	r.Write("s.txt", "later\n")
	r.Commit("side later")
	r.Git("switch", "-q", "main")
	h.trust.Set(r.GitDir(), true)
	h.lib.Opened(r.Dir, h.app.Now())
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("5 commits"))

	h.click("Filter")
	h.click("Message")
	h.tt.Type("NEEDLE")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the match", func() bool { return h.tt.HasText("1 commit") && h.tt.HasText("side needle") })
	if !h.tt.HasText("side needle") || h.tt.HasText("main work") {
		t.Fatalf("filtered rows %q", h.tt.Texts())
	}
	h.click("Clear")
	h.until("every commit", h.shows("5 commits"))

	h.click("Hide merges")
	h.until("no merge", h.shows("4 commits"))
	if h.tt.HasText("merge side") {
		t.Fatalf("merge shown: %q", h.tt.Texts())
	}
	h.click("Hide merges")
	h.until("every commit", h.shows("5 commits"))

	h.menu("side", "Hide in History")
	h.until("side hidden", h.shows("4 commits"))
	if h.tt.HasText("side later") {
		t.Fatalf("hidden branch still shown: %q", h.tt.Texts())
	}
	h.click("First parent only")
	h.until("main's line", h.shows("3 commits"))
	if h.tt.HasText("side needle") {
		t.Fatalf("second parent followed: %q", h.tt.Texts())
	}
	h.click("First parent only")
	h.menu("side", "Pin to Top")
	tb := h.app.current()
	if got := tb.sideRefs.build(tb).heads; !slices.Equal(got, []string{"refs/heads/side", "refs/heads/main"}) {
		t.Fatalf("branches %v", got)
	}
	h.until("prefs saved", func() bool {
		p, hidden := h.lib.RefPrefs(r.Dir)
		return slices.Equal(p, []string{"refs/heads/side"}) && slices.Equal(hidden, []string{"refs/heads/side"})
	})

	// Opened again, the branch is still hidden.
	h.app.Close(0)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("side still hidden", func() bool {
		hs := h.app.current().s.History
		return hs.Done && h.tt.HasText("4 commits") && hs.View > 0
	})
	h.until("the branch details", func() bool { return h.app.current().refs.info != nil })
	h.menu("side", "Show Only This in History")
	h.until("side alone", h.shows("side later"))
	if h.tt.HasText("main work") {
		t.Fatalf("main shown with side alone: %q", h.tt.Texts())
	}
}
