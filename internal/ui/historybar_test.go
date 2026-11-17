package ui

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// History opens on the checked-out commit, its detail shown; the keys
// of the list copy the chosen hash and go to its parent and its child.
func TestHistoryStartsAtHead(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	r.Write("b.txt", "beta\n")
	r.Commit("add beta")
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
	child := strings.TrimSpace(r.Git("rev-parse", "main"))
	// HEAD is not the top row: main's commit is newer.
	h.until("HEAD chosen", func() bool { return hex.EncodeToString(hv.chosenID) == head && hv.chosen == 1 })
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
	h.until("the parent", func() bool { return hex.EncodeToString(hv.chosenID) == parent })
	// The child is read with the commit's relations, a moment after it.
	h.until("its child known", func() bool { return hv.detail.rel != nil && len(hv.detail.rel.Children) > 0 })
	h.tt.Key(ui.Alt, ui.KeyUp)
	h.until("the child", func() bool { return hex.EncodeToString(hv.chosenID) == head })
	h.until("its child known", func() bool {
		return hv.detail.rel != nil && len(hv.detail.rel.Children) > 0 && hex.EncodeToString(hv.detail.id) == head
	})
	h.tt.Key(ui.Alt, ui.KeyUp)
	h.until("main's commit", func() bool { return hex.EncodeToString(hv.chosenID) == child })
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
