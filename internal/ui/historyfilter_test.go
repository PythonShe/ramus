package ui

import (
	"slices"
	"testing"
	"time"

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
	h.until("open", h.shows("No local changes"))
	h.click("History")
	h.until("the commits", h.shows("5 commits"))

	h.click("Filter")
	h.click("Message")
	h.tt.Type("NEEDLE")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the match", func() bool { return h.tt.HasText("1 commit") && h.tt.HasText("side needle") })
	if !shownRow(h, "side needle") || shownRow(h, "main work") {
		t.Fatalf("filtered rows %q", h.tt.Texts())
	}
	h.click("Clear")
	h.until("every commit", h.shows("5 commits"))

	h.click("Hide merges")
	h.until("no merge", h.shows("4 commits"))
	if shownRow(h, "merge side") {
		t.Fatalf("merge shown: %q", h.tt.Texts())
	}
	h.click("Hide merges")
	h.until("every commit", h.shows("5 commits"))

	h.menu("side", "Hide in History")
	h.until("side hidden", h.shows("4 commits"))
	if shownRow(h, "side later") {
		t.Fatalf("hidden branch still shown: %q", h.tt.Texts())
	}
	h.click("First parent only")
	h.until("main's line", h.shows("3 commits"))
	if shownRow(h, "side needle") {
		t.Fatalf("second parent followed: %q", h.tt.Texts())
	}
	h.click("First parent only")
	h.menu("side", "Pin to Top")
	tb := h.app.current()
	if got := tb.sideRefs.build(tb).heads; !slices.Equal(got, []string{"refs/heads/side", "refs/heads/main"}) {
		t.Fatalf("branches %v", got)
	}
	if sr := tb.sideRefs.build(tb); !sr.hidden["refs/heads/side"] || sr.hidden["refs/heads/main"] {
		t.Fatalf("hidden in the sidebar %v", sr.hidden)
	}
	h.until("prefs saved", func() bool {
		p, hidden := h.lib.RefPrefs(r.Dir)
		return slices.Equal(p, []string{"refs/heads/side"}) && slices.Equal(hidden, []string{"refs/heads/side"})
	})

	// Opened again, the branch is still hidden.
	h.app.Close(0)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	h.click("History")
	h.until("side still hidden", func() bool {
		hs := h.app.current().s.History
		return hs.Done && h.tt.HasText("4 commits") && hs.View > 0
	})
	h.until("the branch details", func() bool { return h.app.current().refs.info != nil })
	h.menu("side", "Show Only This in History")
	h.until("side alone", h.shows("side later"))
	if shownRow(h, "main work") {
		t.Fatalf("main shown with side alone: %q", h.tt.Texts())
	}
}

// What is typed in the filter bar applies when the Filter button closes
// it before typing paused, and an error said of a date typed before goes
// once the date is typed back to the filter applied.
func TestFilterClosedAndRetyped(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("alpha work")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("2 commits"))
	hf := &h.app.current().hist.filter

	h.click("Filter")
	h.click("Message")
	h.tt.Type("alpha")
	h.tt.Frame()
	// The Tester's clock stands still: typing has not paused.
	h.click("Filter")
	if hf.open || hf.pending() {
		t.Fatalf("closed with %q typed and not applied", hf.message)
	}
	h.until("the match", h.shows("1 commit"))

	h.click("Filter (1)")
	h.tt.Frame()
	since, ok := h.tt.AccessByRole(ui.RoleTextField, "Since")
	if !ok {
		t.Fatal("no Since field")
	}
	h.tt.ClickAt(since.Bounds.X+since.Bounds.W/2, since.Bounds.Y+since.Bounds.H/2)
	h.tt.Frame()
	h.tt.Type("202")
	h.tt.Advance(time.Second)
	h.until("the date's error", h.shows("Dates are written as 2024-01-31"))
	for range 3 {
		h.tt.Key(0, ui.KeyBackspace)
	}
	h.tt.Frame()
	h.tt.Advance(time.Second)
	h.until("no error", func() bool { return !h.tt.HasText("Dates are written as 2024-01-31") })
	if hf.err != "" {
		t.Fatalf("error %q", hf.err)
	}
}

// shownRow reports whether history lists a commit with subject: in its
// rows, not the chosen commit's detail beside them, which names its
// parents.
func shownRow(h *harness, subject string) bool {
	tb := h.app.current()
	hs := tb.s.History
	if hs.Snap == nil {
		return false
	}
	for i := range hs.Snap.Len() {
		if s, _, _, _, ok := h.app.historyRow(tb, hs, i); ok && s == subject {
			return true
		}
	}
	return false
}
