package ui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestHistoryView(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("tag", "-a", "-m", "first", "v1")
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	r.Git("branch", "topic")
	r.Write("README", "hello again\n")
	r.Commit("edit readme")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("edit readme"))
	for _, s := range []string{"add alpha", "one", "main", "topic", "v1", "3 commits"} {
		if !h.tt.HasText(s) {
			t.Fatalf("missing %q in %q", s, h.tt.Texts())
		}
	}

	h.click("add alpha")
	h.until("the detail", h.shows("1 file changed"))
	h.click("a.txt")
	h.until("its patch", h.shows("+alpha"))
	h.click("Copy Hash")
	if want := strings.TrimSpace(r.Git("rev-parse", "HEAD~1")); h.tt.Clipboard() != want {
		t.Fatalf("copied %q, want %s", h.tt.Clipboard(), want)
	}
	h.click("Copy Patch")
	h.until("the patch copied", func() bool { return strings.Contains(h.tt.Clipboard(), "Subject: [PATCH] add alpha") })

	// Jumping by ref chooses the commit and shows it.
	tab := h.app.current()
	if err := h.tt.Click("Go to hash or ref"); err != nil {
		t.Fatal(err)
	}
	h.tt.Type("v1")
	h.tt.Key(0, ui.KeyEnter)
	want := strings.TrimSpace(r.Git("rev-parse", "v1^{commit}"))
	h.until("the jump", func() bool {
		return tab.hist.chosen == 2 && h.tt.HasText(want)
	})

	// A new commit reloads history and keeps the chosen commit chosen.
	r.Write("b.txt", "beta\n")
	r.Commit("add beta")
	h.until("the new commit", h.shows("add beta"))
	h.until("the choice kept", func() bool { return tab.hist.chosen == 3 })

	if err := h.tt.RightClick("add beta"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Copy Message"); err != nil {
		t.Fatal(err)
	}
	h.until("the message copied", func() bool { return h.tt.Clipboard() == "add beta\n" })
}
