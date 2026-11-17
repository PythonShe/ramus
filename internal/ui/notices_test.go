package ui

import (
	"testing"

	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// A write that fails says so in its tab, with git's reason, until it is
// dismissed or a write ends well; Show in Log opens its entry.
func TestErrorBarShowsFailedWrite(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("switch", "-q", "-c", "topic")
	r.Write("README", "topic")
	r.Commit("on topic")
	r.Git("switch", "-q", "main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("README", "mine")
	tb := h.app.current()
	h.until("the change", func() bool { return tb.s.State.Status != nil && tb.s.State.Status.Len() == 1 })
	switchTopic := func() {
		t.Helper()
		if _, err := tb.s.Submit(write.Switch(tb.s.Where(), "topic", tb.s.Seen())); err != nil {
			t.Fatal(err)
		}
	}
	const failed = "Switch to topic failed: it would overwrite your uncommitted changes"
	switchTopic()
	h.until("the failure", h.shows(failed))
	h.click("Show in Log")
	if tb.sel != "log" || tb.logChosen != 0 {
		t.Fatalf("shows %q, entry %d", tb.sel, tb.logChosen)
	}
	h.until("the entry", h.shows("switch --no-guess --end-of-options topic"))
	h.click("Dismiss Message")
	h.until("dismissed", func() bool { return !h.tt.HasText(failed) })

	switchTopic()
	h.until("the failure again", h.shows(failed))
	ok := write.Op{Name: "Look", Cmd: run.Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"status"}}}
	if _, err := tb.s.Submit(ok); err != nil {
		t.Fatal(err)
	}
	h.until("cleared by a write that ended well", func() bool { return !h.tt.HasText(failed) })
}

// A notice shows in the tab it was raised in, not in another, and goes
// when dismissed or when a write there ends well.
func TestErrorBarNoticeBelongsToItsTab(t *testing.T) {
	h := newHarness(t)
	one, two := h.repoWith("a.txt"), h.repoWith("b.txt")
	h.trust.Set(one.GitDir(), true)
	h.trust.Set(two.GitDir(), true)
	h.app.Open(one.Dir)
	h.until("the first", h.shows("a.txt"))
	first := h.app.current()
	h.app.notice = "the first broke"
	h.until("its notice", h.shows("the first broke"))
	h.app.Open(two.Dir)
	h.until("the second", h.shows("b.txt"))
	if h.tt.HasText("the first broke") {
		t.Fatal("the other tab shows the notice")
	}
	second := h.app.current()

	// Work for the first tab that ends while the second is shown.
	h.app.tell(first, "the first broke later")
	h.tt.Frame()
	if h.tt.HasText("the first broke later") {
		t.Fatal("the notice shows in the tab shown, not its own")
	}
	h.app.active = 0
	h.until("back in its tab", h.shows("the first broke later"))
	h.click("Dismiss Message")
	h.until("dismissed", func() bool { return !h.tt.HasText("the first broke later") && h.app.notice == "" })

	h.app.active = 1
	h.tt.Frame()
	h.app.notice = "the second broke"
	h.until("the second's notice", h.shows("the second broke"))
	ok := write.Op{Name: "Look", Cmd: run.Cmd{GitDir: two.GitDir(), WorkTree: two.Dir, Args: []string{"status"}}}
	if _, err := second.s.Submit(ok); err != nil {
		t.Fatal(err)
	}
	h.until("cleared by a write that ended well", func() bool { return !h.tt.HasText("the second broke") && h.app.notice == "" })

	// Notify speaks to the window, which every tab shows.
	h.app.Notify("for the window")
	h.until("in this tab", h.shows("for the window"))
	h.app.active = 0
	h.until("and in the other", h.shows("for the window"))
}

// A message longer than its line shows whole under Details, to copy,
// in the welcome window as in a tab.
func TestNoticeDetails(t *testing.T) {
	h := newHarness(t)
	h.tt.Frame()
	const msg = "hint: run this first\nfatal: the clone broke"
	h.app.Notify(msg)
	h.until("the line", h.shows("the clone broke"))
	h.click("Details")
	if !h.tt.HasText(msg) {
		t.Fatalf("whole message not shown: %q", h.tt.Texts())
	}
	h.click("Copy")
	h.tt.Frame()
	if got := h.tt.Clipboard(); got != msg {
		t.Fatalf("copied %q", got)
	}
}
