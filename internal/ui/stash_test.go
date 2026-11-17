package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The stash command has a key of its own, and Enter on a stash, as a
// double click, pops it, as its menu's Pop does.
func TestStashKeyAndPop(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	readme := func() string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(r.Dir, "README"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	stash := func(text string) {
		t.Helper()
		r.Write("README", text)
		h.until("the change", func() bool { st := tb.s.State.Status; return st != nil && st.Len() > 0 })
		h.tt.Key(ui.Cmd|ui.Shift, ui.KeyS)
		h.until("the dialog", h.shows("Include untracked files"))
		h.tt.Type(text)
		h.click("Stash")
		h.idle(tb, "the stash")
		h.until("no changes", func() bool { st := tb.s.State.Status; return st != nil && st.Len() == 0 })
	}
	stash("first")
	stash("second")
	h.until("the stashes item", h.shows("Stashes (2)"))
	h.click("Stashes (2)")
	h.until("the stashes", func() bool { return hasExact(h, "On main: second") && hasExact(h, "On main: first") })

	// Chosen by its entry: the detail beside the list shows its message
	// too. The Tester has no double click; Enter is the same action.
	h.click("stash@{1}")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.idle(tb, "the pop")
	h.until("one stash left", h.shows("Stashes (1)"))
	if got := readme(); got != "first" {
		t.Fatalf("README %q", got)
	}
	if got := strings.TrimSpace(r.Git("stash", "list")); !strings.HasSuffix(got, "On main: second") {
		t.Fatalf("stashes left %q", got)
	}
}

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
	h.until("the repository", h.shows("No local changes"))
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

// The stash dialog names how many files it stashes, as its choices
// change that, and stashes none while it would take none.
func TestStashTitleCounts(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("f.txt", "f\n")
	r.Commit("one file")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("new1.txt", "1\n")
	r.Write("new2.txt", "2\n")
	h.until("the changes", h.shows("new2.txt"))
	h.app.openStash(h.app.current())
	h.until("the dialog", h.shows("Stash Changes to 2 Files"))
	h.click("Include untracked files")
	h.until("none taken", h.shows("Stash Changes to 0 Files"))
	if b, ok := h.tt.Access("Stash"); !ok || !b.Disabled {
		t.Fatalf("Stash offered for no file: %+v", b)
	}
	h.click("Include untracked files")
	r.Write("f.txt", "changed\n")
	h.until("three", h.shows("Stash Changes to 3 Files"))
}
