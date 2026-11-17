package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Any commit compares with HEAD from its menu: the files changed between
// them, and a file's patch from one to the other.
func TestCompareCommits(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a1\n")
	r.Commit("add a")
	r.Write("a.txt", "a2\n")
	r.Write("b.txt", "b\n")
	r.Commit("change a, add b")
	r.Write("c.txt", "c\n")
	r.Commit("add c")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.click("History")
	h.until("history", h.shows("add a"))
	h.menu("add a", "Compare with HEAD")
	h.until("the comparison", h.shows("3 files changed"))
	tb := h.app.current()
	h.until("a's patch", patchHas(&tb.cmp.cv.pv, "a2", "a1"))
	// Each side shows its subject; the first file's patch stays open,
	// with nothing to close it to.
	h.until("the subjects", func() bool { return h.tt.HasText("add a") && h.tt.HasText("add c") })
	if slices.ContainsFunc(h.tt.AccessAll(), func(a ui.AccessInfo) bool { return strings.HasPrefix(a.Name, "Close Patch") }) {
		t.Fatal("the patch can be closed onto nothing")
	}
	if !strings.HasPrefix(tb.cmp.fromName, strings.TrimSpace(r.Git("rev-parse", "--short=10", "HEAD~2"))) {
		t.Fatalf("from %q", tb.cmp.fromName)
	}
	// A file is restored as the newer side has it, which its item names.
	h.menu("b.txt", "Restore File as of HEAD…")
	h.until("the question", h.shows("Restore b.txt?"))
	h.click("Cancel")
	h.click("Swap")
	h.until("the other way", func() bool { return tb.cmp.fromName == "HEAD" && h.tt.HasText("3 files changed") })
	h.until("a's patch reversed", patchHas(&tb.cmp.cv.pv, "a1", "a2"))
}

// A commit compares with the working tree: what changed since it, staged
// or not, read again as the files change; nothing there to restore from
// or swap.
func TestCompareWithWorkTree(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a1\n")
	r.Commit("add a")
	r.Write("b.txt", "b\n")
	r.Commit("add b")
	r.Write("a.txt", "a2\n")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("Changes (1)"))
	h.click("History")
	h.until("history", h.shows("add a"))
	h.menu("add a", "Compare with Working Tree")
	tb := h.app.current()
	h.until("the comparison", func() bool {
		d := tb.cmp.d
		return d != nil && d.Done && d.Files.Len() == 2 && h.tt.HasText("Working Tree")
	})
	h.until("a's patch", patchHas(&tb.cmp.cv.pv, "a2", "a1"))
	if _, ok := h.tt.Access("Swap"); ok {
		t.Fatal("the work tree can be swapped")
	}
	if err := h.tt.RightClick("b.txt"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	for _, item := range h.tt.Menu() {
		if strings.HasPrefix(item, "Restore") {
			t.Fatalf("restore offered from the work tree: %q", h.tt.Menu())
		}
	}
	h.tt.Key(0, ui.KeyEscape)

	r.Write("a.txt", "a3\n")
	h.until("the change read", patchHas(&tb.cmp.cv.pv, "a3", "a1"))
}

// A branch compared with the one checked out shows what it changes since
// it split off, as its pull request would, or how the two tips differ;
// swapped, what the other changes since; a branch sharing no history
// with it compares the tips, and says so.
func TestCompareBranches(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("base")
	r.Git("checkout", "-q", "-b", "topic")
	r.Write("t.txt", "topic\n")
	r.Commit("on topic")
	r.Git("checkout", "-q", "main")
	r.Write("m.txt", "main\n")
	r.Commit("on main")
	r.Git("checkout", "-q", "--orphan", "alone")
	r.Git("rm", "-q", "-rf", ".")
	r.Write("z.txt", "z\n")
	r.Commit("alone")
	r.Git("checkout", "-q", "-f", "main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("Branches and Tags")
	h.until("the branches", func() bool { return tb.refs.info != nil })
	h.tt.Frame()

	files := func(want ...string) func() bool {
		return func() bool {
			d := tb.cmp.d
			if d == nil || !d.Done || d.Files == nil || d.Files.Len() != len(want) {
				return false
			}
			for i, w := range want {
				if string(d.Files.Path(i)) != w {
					return false
				}
			}
			return true
		}
	}
	h.menu("topic", "Compare with main")
	// Left while it reads where they split, it reads it again when shown.
	if !tb.cmp.basing {
		t.Fatal("the merge base not being read")
	}
	h.app.hideTab(tb)
	h.until("topic's own change", files("t.txt"))
	h.click("Tip to Tip")
	h.until("the tips, main's change reversed", files("m.txt", "t.txt"))
	h.click("Since Branching Off")
	h.until("topic's own change again", files("t.txt"))
	h.click("Swap")
	h.until("main's own change", func() bool { return tb.cmp.fromName == "topic" && files("m.txt")() })

	h.click("Branches and Tags")
	h.menu("alone", "Compare with main")
	h.until("the tips, said why", func() bool {
		return files("README", "a.txt", "m.txt", "z.txt")() && h.tt.HasText("main and alone share no history, so their tips are compared.")
	})
}
