package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/write"
)

// A double click on a branch in the sidebar of a repository opened
// read-only switches to nothing and says nothing, local branch or remote,
// as their menu items are off there. The Tester has no double click: the
// test calls what one runs.
func TestSidebarSwitchReadOnly(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("branch", "topic")
	r := h.repoWith()
	r.Git("branch", "other")
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	h.app.Open(r.Dir)
	h.until("the banner", h.shows("Read-only"))
	tb := h.app.current()
	h.until("the sidebar's branches", func() bool { return h.tt.HasText("origin/topic") && hasExact(h, "other") })
	if tb.s.Queue != nil {
		t.Fatal("an untrusted repository opened writable")
	}
	for _, ref := range []string{"refs/heads/other", "refs/remotes/origin/topic"} {
		h.app.sidebarSwitch(tb, ref)
		h.tt.Frame()
		if h.app.notice != "" || len(tb.s.Log) > 0 {
			t.Fatalf("%s: notice %q, log %d", ref, h.app.notice, len(tb.s.Log))
		}
	}
	if head := tb.s.State.Head.Target; head != "refs/heads/main" {
		t.Fatalf("switched to %s", head)
	}
}

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

// Two branches whose names read alike once what could mislead is taken
// out are two commands in the palette, each switching to its own.
func TestPaletteBranchesReadAlike(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "topic")
	r.Git("branch", "topic​")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.until("the refs", func() bool { return tb.s.State.Refs != nil && len(tb.sideRefs.build(tb).heads) == 3 })
	h.app.palette.open = true
	h.tt.Frame()
	h.tt.Type("switch to topic")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.until("on the second", func() bool { return tb.s.State.Head.Target == "refs/heads/topic​" })
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
