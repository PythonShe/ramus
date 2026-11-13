package ui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// An input method's Enter ends its composition: the platform keeps that
// key from the view and commits the text, which submits nothing. Only an
// Enter after it submits the field, and no view acts on Enter keys of its
// own, past what a field submits.
func TestEnterWhileComposing(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("README", "changed\n")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("modified README"))
	tb := h.app.current()
	h.app.openStash(tb)
	h.until("the dialog", h.shows("Stash Changes"))
	for _, part := range []string{"に", "にほ", "にほん"} {
		h.tt.Compose(part, len([]rune(part)))
		h.tt.Frame()
	}
	h.tt.Type("日本") // the input method's Enter
	h.tt.Frame()
	if !h.app.stashUI.open || h.app.stashUI.message != "日本" {
		t.Fatalf("composing submitted the stash, or lost its text: open %v, %q", h.app.stashUI.open, h.app.stashUI.message)
	}
	h.tt.Key(0, ui.KeyEnter)
	h.until("stashed", func() bool { return strings.Contains(r.Git("stash", "list"), "日本") })

	// Composing in the commit summary types the text and commits nothing.
	// git stash writes its ref before it resets the work tree.
	h.idle(tb, "the stash")
	r.Write("README", "again\n")
	r.Git("add", "README")
	h.until("staged", h.shows("staged modified README"))
	if err := h.tt.Click("Summary"); err != nil {
		t.Fatal(err)
	}
	h.tt.Compose("かい", 2)
	h.tt.Frame()
	h.tt.Type("改")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if tb.commit.subject != "改" || tb.commit.busy {
		t.Fatalf("summary %q, committing %v", tb.commit.subject, tb.commit.busy)
	}
	if n := strings.Count(r.Git("log", "--format=%s"), "\n"); n != 1 {
		t.Fatalf("%d commits", n)
	}
}
