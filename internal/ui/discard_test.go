package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Discarding a file added with intent to add takes the intent back and
// leaves its content: putting it back as the index has it would empty it.
func TestDiscardIntentToAdd(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("ita.txt", "my work\n")
	r.Git("add", "-N", "ita.txt")
	h.until("the change", h.shows("ita.txt"))
	tb := h.app.current()
	st := tb.s.State.Status
	found := false
	for i := range st.Len() {
		if string(st.Path(i)) == "ita.txt" {
			found = intentToAdd(st, i)
		}
	}
	if !found {
		t.Fatal("ita.txt is not seen as added with intent to add")
	}
	logged := len(tb.s.Log)
	h.app.askDiscardPaths(tb, &tb.cv.confirm, discardSet{intent: []string{"ita.txt"}})
	h.until("the question", h.shows("Discard 1 file?"))
	h.click("Discard")
	h.until("the write", func() bool { return len(tb.s.Log) > logged && !tb.s.Log[len(tb.s.Log)-1].Running })
	if e := tb.s.Log[len(tb.s.Log)-1]; e.Err != nil {
		t.Fatalf("%s: %v", e.Name, e.Err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "ita.txt")); string(b) != "my work\n" {
		t.Fatalf("ita.txt is %q", b)
	}
	if ls := strings.TrimSpace(r.Git("ls-files", "ita.txt")); ls != "" {
		t.Fatalf("ita.txt is still in the index: %q", ls)
	}
}

// Discarding tracked and untracked files together does both: the delete
// of the untracked ones, queued after the discard, does not find the
// tracked files it changed and refuse as stale.
func TestDiscardTrackedAndUntracked(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("a.txt", "changed\n")
	r.Write("new.txt", "x\n")
	h.until("the changes", func() bool { return h.tt.HasText("a.txt") && h.tt.HasText("new.txt") })
	tb := h.app.current()
	logged := len(tb.s.Log)

	h.app.askDiscardPaths(tb, &tb.cv.confirm, discardSet{tracked: []string{"a.txt"}, untracked: []string{"new.txt"}})
	h.until("the question", h.shows("Discard 2 files?"))
	h.click("Discard")
	h.until("both writes", func() bool {
		if len(tb.s.Log) < logged+2 {
			return false
		}
		for _, e := range tb.s.Log[logged:] {
			if e.Running {
				return false
			}
		}
		return true
	})
	for _, e := range tb.s.Log[logged:] {
		if e.Err != nil {
			t.Errorf("%s: %v", e.Name, e.Err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "a.txt")); string(b) != "a\n" {
		t.Errorf("a.txt is %q", b)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("new.txt is still there: %v", err)
	}
}

// Discard All Changes takes the work tree and the index back to HEAD:
// staged changes are unstaged and discarded, a file staged as new is
// deleted, though changed again since, and untracked files deleted.
func TestDiscardAllChanges(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Write("b.txt", "b\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("a.txt", "staged\n")
	r.Git("add", "a.txt")
	r.Write("a.txt", "staged and more\n")
	r.Write("b.txt", "changed\n")
	r.Write("added.txt", "new\n")
	r.Git("add", "added.txt")
	r.Write("added.txt", "new, changed\n")
	r.Write("loose.txt", "x\n")
	h.until("the changes", h.shows("loose.txt"))
	if err := h.tt.Click("More"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Discard All Changes…"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", h.shows("Discard 4 files?"))
	h.click("Discard")
	h.until("nothing changed", func() bool { return r.Git("--no-optional-locks", "status", "--porcelain") == "" })
	for f, want := range map[string]string{"a.txt": "a\n", "b.txt": "b\n"} {
		if b, _ := os.ReadFile(filepath.Join(r.Dir, f)); string(b) != want {
			t.Errorf("%s is %q", f, b)
		}
	}
}

// In a clean tree the More menu offers nothing that needs a change.
// Clean stays: it may delete ignored files, which status does not list.
func TestMoreMenuWithNothingChanged(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	for _, item := range []string{"Stash Changes…", "Discard All Changes…"} {
		if err := h.tt.Click("More"); err != nil {
			t.Fatal(err)
		}
		if err := h.tt.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Errorf("%s: %v", item, err)
		}
		h.tt.Key(0, ui.KeyEscape)
		h.tt.Frame()
	}
}

// A staged file's menu discards its change, unstaging it first.
func TestDiscardStagedFile(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("a")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	r.Write("a.txt", "staged\n")
	r.Git("add", "a.txt")
	h.until("the change", h.shows("Staged (1)"))
	h.menu("a.txt", "Discard Changes to File…")
	h.until("the question", h.shows("Discard 1 file?"))
	h.click("Discard")
	h.until("nothing changed", func() bool { return r.Git("--no-optional-locks", "status", "--porcelain") == "" })
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "a.txt")); string(b) != "a\n" {
		t.Errorf("a.txt is %q", b)
	}
}

// Discarding a file staged and changed again keeps both versions in one
// snapshot, which Undo puts back: what was staged is not lost.
func TestDiscardStagedKeepsTheIndex(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("f.txt", "base\n")
	r.Commit("base")
	r.Write("f.txt", "staged only\n")
	r.Git("add", "f.txt")
	r.Write("f.txt", "work tree\n")
	blob := strings.TrimSpace(r.Git("rev-parse", ":f.txt"))
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("staged", h.shows("Staged (1)"))
	tb := h.app.current()
	logged := len(tb.s.Log)
	h.menu("f.txt", "Discard Changes to File…")
	h.until("the question", h.shows("Discard 1 file?"))
	h.click("Discard")
	h.until("nothing changed", func() bool { return r.Git("--no-optional-locks", "status", "--porcelain") == "" })
	h.idle(tb, "the discard done")
	writes := len(tb.s.Log) - logged
	h.app.undo(tb, false)
	h.idle(tb, "the undo done")
	if out, _ := r.Run(nil, "rev-parse", ":f.txt"); strings.TrimSpace(out) != blob {
		t.Errorf("staged %s, want %s", out, blob)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "f.txt")); string(b) != "work tree\n" {
		t.Errorf("f.txt is %q", b)
	}
	if writes != 1 {
		t.Errorf("%d writes, want one", writes)
	}
}
