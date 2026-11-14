package ui

import (
	"strings"
	"testing"
)

// Stash the changes to one file, then what is staged alone; both show
// in the list with their messages.
func TestStashPicked(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("f.txt", "f\n")
	r.Write("g.txt", "g\n")
	r.Commit("two files")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	r.Write("f.txt", "f changed\n")
	r.Write("g.txt", "g changed\n")
	h.until("the changes", h.shows("g.txt"))

	h.menu("f.txt", "Stash Changes to File…")
	h.until("the dialog", h.shows("Stash Changes to f.txt"))
	h.tt.Type("just f")
	h.click("Stash")
	h.idle(tb, "the stash")
	if got := strings.TrimSpace(r.Git("status", "--porcelain")); got != "M g.txt" {
		t.Fatalf("status %q", got)
	}

	r.Git("add", "g.txt")
	r.Write("g.txt", "g changed\nand more\n")
	h.until("g staged", h.shows("Staged (1)"))
	if !canStashStaged(tb) {
		t.Skip("no stash push --staged in this git")
	}
	h.menu("g.txt", "Stash Staged Changes…")
	h.until("the staged dialog", func() bool { return h.app.stashUI.open && h.app.stashUI.what == 1 })
	if h.tt.HasText("Include untracked files") {
		t.Fatal("untracked offered for a stash of what is staged")
	}
	h.tt.Type("staged g")
	h.click("Stash")
	h.idle(tb, "the staged stash")
	// What was staged is stashed; the change made after stays.
	if got := r.Git("diff", "stash@{0}^1", "stash@{0}", "--", "g.txt"); !strings.Contains(got, "+g changed") || strings.Contains(got, "and more") {
		t.Fatalf("stash holds\n%s", got)
	}
	h.until("the second stash", h.shows("Stashes (2)"))
	h.click("Stashes (2)")
	h.until("both stashes", func() bool { return h.tt.HasText("On main: just f") && h.tt.HasText("On main: staged g") })
}
