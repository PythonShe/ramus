package ui

import (
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/write"
)

// A remote branch's menu offers what a local branch's does, and deleting
// it where it lives, which asks first.
func TestRemoteBranchMenu(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("branch", "topic")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the branches", func() bool { return tb.refs.info != nil && h.tt.HasText("origin/topic") })
	if err := h.tt.RightClick("origin/topic"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	menu := h.tt.Menu()
	for _, want := range []string{"Switch", "Create Branch Here…", "Create Tag Here…", "Delete on origin…", "Copy Name"} {
		if !slices.Contains(menu, want) {
			t.Fatalf("no %q in %q", want, menu)
		}
	}
	if err := h.tt.ChooseMenuItem("Delete on origin…"); err != nil {
		t.Fatal(err)
	}
	h.until("the confirmation", h.shows("Delete topic on origin?"))
	h.click("Cancel")
	if out := up.Git("branch", "--list", "topic"); !strings.Contains(out, "topic") {
		t.Fatal("deleted without being confirmed")
	}

	// A pull request's ref fetched under origin/pr/ maps from no branch:
	// a branch pr/1 the remote may have is another thing.
	up.Git("update-ref", "refs/pull/1/head", "HEAD")
	r.Git("config", "--add", "remote.origin.fetch", "+refs/pull/*/head:refs/remotes/origin/pr/*")
	r.Git("fetch", "-q", "origin")
	h.until("the pull request's ref", func() bool { return h.tt.HasText("origin/pr/1") })
	if err := h.tt.RightClick("origin/pr/1"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if err := h.tt.ChooseMenuItem("Delete on origin…"); err == nil {
		t.Fatal("a pull request's ref offered to delete as branch pr/1")
	}
}

// The palette lists Switch to for the branches a query names only.
func TestPaletteBranchCommands(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "feature/alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.until("the refs", func() bool { return tb.s.State.Refs != nil && len(tb.sideRefs.build(tb).heads) == 2 })
	has := func(query, label string) bool {
		for _, c := range match(h.app.commands(query), query) {
			if c.label == label {
				return true
			}
		}
		return false
	}
	if has("", "Switch to feature/alpha") {
		t.Fatal("a branch command with no query")
	}
	if !has("alpha", "Switch to feature/alpha") || has("alpha", "Switch to main") {
		t.Fatal("the query does not pick the branch")
	}
	if has("stashes", "Show Stashes") {
		t.Fatal("Show Stashes with no stash")
	}
}

func TestDeleteRemoteBranchOp(t *testing.T) {
	tip := strings.Repeat("ab", 20)
	op, err := write.DeleteRemoteBranch(write.Where{}, "origin", "topic", tip)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(op.Cmd.Args, " "); got != "push --progress --force-with-lease=refs/heads/topic:"+tip+" --delete -- origin refs/heads/topic" {
		t.Fatalf("args %q", got)
	}
	if _, err := write.DeleteRemoteBranch(write.Where{}, "origin", "-x", tip); err == nil {
		t.Fatal("an option as a branch name")
	}
}

// A name no branch has shows the row that makes it, and Enter outcome
// is no surprise.
func TestBranchPickerCreateRow(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.click("Branch main")
	h.tt.Type("zzz")
	h.until("the row", h.shows("Create Branch zzz…"))
}
