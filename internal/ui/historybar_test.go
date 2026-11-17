package ui

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// History opens on the checked-out commit, its detail shown; the keys
// of the list copy the chosen hash and go to its parent and its newest
// child, with commits of other branches between them in the list.
func TestHistoryStartsAtHead(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("switch", "-q", "-c", "side")
	r.Write("s.txt", "side\n")
	r.Commit("side work")
	r.Git("switch", "-q", "main")
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	r.Write("b.txt", "beta\n")
	r.Commit("add beta")
	r.Git("switch", "-q", "-c", "newer", "HEAD~1")
	r.Write("c.txt", "gamma\n")
	r.Commit("add gamma")
	r.Git("switch", "-q", "-c", "older", "HEAD~1")
	r.Git("branch", "aaa")
	r.Git("tag", "v1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	tb := h.app.current()
	hv := &tb.hist
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	parent := strings.TrimSpace(r.Git("rev-parse", "HEAD~1"))
	child := strings.TrimSpace(r.Git("rev-parse", "newer"))
	// HEAD is not the top row: its children are newer, and side's commit
	// is between it and its parent.
	h.until("HEAD chosen", func() bool { return hex.EncodeToString(hv.chosenID) == head && hv.chosen == 2 })
	h.until("its detail", h.shows("1 file changed"))
	// Its row names the current branch first, whatever its name, then
	// the other branches, then the tags.
	h.until("its labels", func() bool {
		var got []string
		for _, l := range hv.labels[string(hv.chosenID)] {
			got = append(got, l.text)
		}
		return strings.Join(got, " ") == "older aaa v1"
	})

	// A click on the row chosen gives the list the focus.
	h.click("add alpha")
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.until("the hash copied", func() bool { return h.tt.Clipboard() == head })

	h.tt.Key(ui.Alt, ui.KeyDown)
	h.until("the parent", func() bool { return hex.EncodeToString(hv.chosenID) == parent && hv.chosen == 4 })
	// The child is read with the commit's relations, a moment after it.
	h.until("its child known", func() bool { return hv.detail.rel != nil && len(hv.detail.rel.Children) > 0 })
	h.tt.Key(ui.Alt, ui.KeyUp)
	h.until("the child", func() bool { return hex.EncodeToString(hv.chosenID) == head })
	h.until("its child known", func() bool {
		return hv.detail.rel != nil && len(hv.detail.rel.Children) > 0 && hex.EncodeToString(hv.detail.id) == head
	})
	h.tt.Key(ui.Alt, ui.KeyUp)
	h.until("the newest child", func() bool { return hex.EncodeToString(hv.chosenID) == child && hv.chosen == 0 })
}

// History starting at HEAD yields to the user: a row chosen, or the list
// scrolled, before HEAD's row is found stays as the user left it.
func TestHistoryStartAtHeadYields(t *testing.T) {
	open := func(t *testing.T, above int) (*harness, *historyState, string) {
		h := newHarness(t)
		r := h.repoWith()
		r.Write("a.txt", "alpha\n")
		head := r.Commit("add alpha")
		r.Git("branch", "older")
		for i := range above {
			r.Write("b.txt", strings.Repeat("b", i+1))
			r.Commit("add b" + strings.Repeat("!", i))
		}
		r.Git("switch", "-q", "older")
		h.trust.Set(r.GitDir(), true)
		h.app.Open(r.Dir)
		h.until("open", h.shows("No changes"))
		h.click("History")
		hv := &h.app.current().hist
		// Stops before the update that finds HEAD's row.
		h.until("HEAD looked for", func() bool { return hv.want != nil })
		return h, hv, head
	}
	settle := func(h *harness) {
		n := 0
		h.until("settled", func() bool { n++; return n > 30 })
	}

	t.Run("chosen", func(t *testing.T) {
		h, hv, head := open(t, 1)
		// The top row, its text not read yet.
		h.click("commit, loading")
		settle(h)
		if hex.EncodeToString(hv.chosenID) == head || hv.chosen != 0 {
			t.Fatalf("HEAD took the choice back: row %d", hv.chosen)
		}
	})
	t.Run("scrolled", func(t *testing.T) {
		h, hv, head := open(t, 80)
		hv.list.ScrollTo(10, ui.Start)
		h.tt.Frame()
		h.until("HEAD chosen", func() bool { return hex.EncodeToString(hv.chosenID) == head && hv.chosen == 80 })
		settle(h)
		if first, _ := hv.list.Visible(); first != 10 {
			t.Fatalf("the list scrolled from row 10 to %d", first)
		}
	})
}

// The go-to field: a name that is no commit says so until the text
// changes; words are looked for in the messages instead; a commit gone to
// leaves the field empty. Escape closes the filter bar.
func TestHistoryGoTo(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	r.Write("b.txt", "beta\n")
	r.Commit("fix the needle")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("3 commits"))
	tb := h.app.current()
	hv := &tb.hist

	h.click("Go to hash or ref")
	h.tt.Type("abcdef12")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the error", func() bool { return hv.jumpErr != "" && h.tt.HasText(hv.jumpErr) })
	h.tt.Type("3")
	h.until("the error gone", func() bool { return hv.jumpErr == "" })

	hv.jump = ""
	h.tt.Frame()
	h.tt.Type("needle")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the messages searched", func() bool {
		return hv.filter.open && hv.filter.message == "needle" && h.tt.HasText("1 commit") && hv.jump == ""
	})
	if hv.jumpErr != "" {
		t.Fatalf("words typed say %q", hv.jumpErr)
	}
	// A word that could be a short hash, naming no commit, is a word.
	h.click("Go to hash or ref")
	h.tt.Type("decade")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the word searched", func() bool { return hv.filter.message == "decade" && hv.jump == "" })
	if hv.jumpErr != "" {
		t.Fatalf("a hex word says %q", hv.jumpErr)
	}

	// Nothing matches: the way out is beside the message.
	hv.filter.message = "nothing like it"
	h.app.applyHistoryFilter(tb)
	h.until("no match", h.shows("No commits match"))
	h.click("Clear Filter")
	h.until("every commit", h.shows("3 commits"))
	if hv.filter.active() != 0 {
		t.Fatalf("filter left %+v", hv.filter)
	}

	h.click("Message")
	h.tt.Key(0, ui.KeyEscape)
	h.until("the bar closed", func() bool { return !hv.filter.open && !h.tt.HasText("Hide merges") })

	want := strings.TrimSpace(r.Git("rev-parse", "HEAD~1"))
	h.click("Go to hash or ref")
	h.tt.Type(want[:10])
	h.tt.Key(0, ui.KeyEnter)
	h.until("the jump", func() bool { return hex.EncodeToString(hv.chosenID) == want && hv.jump == "" })
}

// What is typed in the filter bar applies once typing pauses, without
// Return.
func TestHistoryFilterAppliesAsTypingPauses(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("2 commits"))
	h.click("Filter")
	h.click("Message")
	h.tt.Type("alpha")
	h.tt.Frame()
	h.tt.Frame()
	if v := h.app.current().s.History; v.Filtered {
		t.Fatal("applied as typed")
	}
	h.tt.Advance(time.Second)
	h.until("applied", func() bool { return h.tt.HasText("1 commit") })
}

// The filter button counts what narrows history: what is typed counts
// once applied, and closing the bar before typing pauses applies it. A
// field typed into and cleared again applies nothing.
func TestHistoryFilterCountsWhatApplies(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("2 commits"))
	hf := &h.app.current().hist.filter
	h.click("Filter")
	h.click("Message")
	h.tt.Type("x")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	h.tt.Frame()
	h.tt.Frame()
	if hf.applied != "" || hf.message != "" {
		t.Fatalf("the empty filter applied: %q", hf.applied)
	}

	h.tt.Type("alpha")
	h.tt.Frame()
	if h.tt.HasText("Filter (1)") {
		t.Fatal("counted before it applies")
	}
	h.tt.Key(0, ui.KeyEscape)
	h.until("applied as the bar closed", func() bool { return !hf.open && h.tt.HasText("1 commit") })
	if !h.tt.HasText("Filter (1)") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
}
