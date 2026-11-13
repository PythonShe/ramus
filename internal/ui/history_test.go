package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/issuelink"
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

func TestIssueLinks(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("remote", "add", "origin", "git@github.com:octo/proj.git")
	r.Write("a.txt", "a\n")
	r.Commit("fix crash\n\nCloses #12 and PROJ-7.")
	h.trust.Set(r.GitDir(), true)
	h.lib.Opened(r.Dir, h.app.Now())
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("fix crash"))
	h.click("fix crash")
	exact := func(s string) func() bool {
		return func() bool { return slices.Contains(h.tt.Texts(), s) }
	}
	h.until("the link from the remote", exact("#12"))
	if exact("PROJ-7")() {
		t.Fatal("PROJ-7 linked before a rule names it")
	}

	tab := h.app.current()
	h.app.openLinks(tab)
	h.tt.Frame()
	h.app.links.rules[0] = issuelink.Rule{Pattern: `\bPROJ-(\d+)\b`, URL: "https://jira.example.com/browse/PROJ-$1"}
	h.click("Save")
	h.until("the rule used", exact("PROJ-7"))
	if got := h.lib.IssueLinks(r.Dir); len(got) != 1 {
		t.Fatalf("saved %+v", got)
	}
}

func TestReflogView(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "lost\n")
	lost := r.Commit("lost work")
	r.Git("reset", "-q", "--hard", "HEAD~1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("Reflog")
	h.until("the reflog", h.shows("reset: moving to HEAD~1"))

	h.click("Lost Commits")
	h.click("Find Lost Commits")
	h.until("the lost commit", h.shows("lost work"))
	h.click("lost work")
	h.until("its detail", h.shows("1 file changed"))
	if err := h.tt.RightClick("lost work"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Create Branch Here…"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Type("rescued")
	h.click("Create")
	h.until("the branch", func() bool {
		return strings.TrimSpace(r.Git("rev-parse", "--abbrev-ref", "HEAD")) == "rescued"
	})
	if got := strings.TrimSpace(r.Git("rev-parse", "rescued")); got != lost {
		t.Fatalf("rescued at %s, want %s", got, lost)
	}

	// Reset the branch back, hard, after a confirmation.
	h.app.current().reflog.lostShown = 0
	h.until("the new entry", h.shows("checkout: moving from main to rescued"))
	if err := h.tt.RightClick("reset: moving to HEAD~1"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Reset rescued to Here", "Hard: Drop Local Changes…"); err != nil {
		t.Fatal(err)
	}
	h.until("the question", h.shows("Reset"))
	h.app.confirm.run()
	h.until("the reset", func() bool {
		return strings.TrimSpace(r.Git("rev-parse", "rescued")) != lost
	})
}
