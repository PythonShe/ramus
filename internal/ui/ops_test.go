package ui

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/run"
)

// repoMenu opens the repository picker and chooses path from the menu
// of the repository named name.
func (h *harness) repoMenu(name string, path ...string) {
	h.t.Helper()
	if !h.app.repoPick.open {
		h.app.openRepoPicker()
	}
	var row ui.AccessInfo
	h.until("the picker's row "+name, func() bool {
		var ok bool
		row, ok = h.tt.AccessByRole(ui.RoleButton, name)
		return ok
	})
	h.tt.RightClickAt(row.Bounds.X+row.Bounds.W/2, row.Bounds.Y+row.Bounds.H/2)
	h.tt.Frame()
	if err := h.tt.ChooseMenuItem(path...); err != nil {
		h.t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.tt.Frame()
}

func (h *harness) menu(row string, path ...string) {
	h.t.Helper()
	// A list read again after a write shows its rows a frame or more
	// later, and the open commit's detail can show a row's subject before
	// the list has the row, as selectable text whose menu is Copy and
	// Select All: the right-click is made again until its menu has the
	// item, the row's coming before the detail's.
	var err error
	var menu []string
	found := false
	defer func() {
		if !found {
			h.t.Logf("last right-click on %q: %v; menu %q", row, err, menu)
		}
	}()
	h.until(fmt.Sprintf("the menu of %q with %q", row, path[0]), func() bool {
		if !h.tt.HasText(row) {
			return false
		}
		if err = h.tt.RightClick(row); err != nil {
			return false
		}
		h.tt.Frame()
		if menu = h.tt.Menu(); slices.Contains(menu, path[0]) {
			found = true
			return true
		}
		h.tt.CloseMenu()
		return false
	})
	if err := h.tt.ChooseMenuItem(path...); err != nil {
		h.t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.tt.Frame()
}

// idle waits for the last write to end and the state to show where HEAD
// went: a write decided on an older HEAD is refused.
func (h *harness) idle(tb *tab, what string) {
	h.t.Helper()
	h.until(what, func() bool {
		l := tb.s.Log
		if len(l) == 0 || l[len(l)-1].Running {
			return false
		}
		// The commit alone does not say the write is shown: a rebase has
		// HEAD at its last commit, detached, before it moves the branch
		// there, and a state read then still has the refs from before.
		out, _ := tb.s.Repo.Runner.Run(h.t.Context(), &run.Cmd{GitDir: tb.s.Repo.GitDir, Read: true, Args: []string{"rev-parse", "HEAD"}})
		ref, _ := tb.s.Repo.Runner.Run(h.t.Context(), &run.Cmd{GitDir: tb.s.Repo.GitDir, Read: true, Args: []string{"symbolic-ref", "-q", "HEAD"}})
		head := tb.s.State.Head
		return strings.TrimSpace(string(out.Stdout)) == hex.EncodeToString(head.OID) &&
			strings.TrimSpace(string(ref.Stdout)) == head.Target
	})
}

// Stash, drop and restore; clean with ignored files.
func TestStashAndClean(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write(".gitignore", "*.log\n")
	r.Commit("ignore")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	r.Write("README", "changed")
	r.Write("new.txt", "x")
	h.until("the changes", h.shows("new.txt"))

	h.click("More")
	if err := h.tt.ChooseMenuItem("Stash Changes…"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the dialog", h.shows("Include untracked files"))
	h.tt.Type("my work")
	h.click("Stash")
	h.until("the stash", h.shows("Stashes (1)"))
	if st := strings.TrimSpace(r.Git("status", "--porcelain")); st != "" {
		t.Fatalf("left %q", st)
	}
	h.click("Stashes (1)")
	// Exactly: the write's progress in the tab bar holds the text too.
	h.until("the stash list", func() bool { return hasExact(h, "On main: my work") })
	stash := strings.TrimSpace(r.Git("rev-parse", "stash@{0}"))
	h.menu("On main: my work", "Drop…")
	h.until("the question", h.shows("Drop stash@{0} ("+stash[:10]+")?"))
	h.click("Drop")
	h.until("dropped", h.shows("Dropped "+stash[:10]))
	h.idle(tb, "the drop")
	h.click("Restore")
	h.until("restored", func() bool {
		out, err := r.Run(nil, "rev-parse", "stash@{0}")
		return err == nil && strings.TrimSpace(out) == stash
	})
	h.idle(tb, "the restore")
	h.until("the restored stash", h.shows("On main: my work"))
	h.menu("On main: my work", "Pop")
	h.idle(tb, "the pop")
	if l := tb.s.Log; l[len(l)-1].Err != nil || h.app.notice != "" {
		t.Fatalf("%s: %v; notice %q; output %q", l[len(l)-1].Name, l[len(l)-1].Err, h.app.notice, l[len(l)-1].Output)
	}
	h.until("popped", func() bool { return strings.Contains(r.Git("status", "--porcelain"), "new.txt") })

	// Clean lists what git would delete; ignored files only when asked.
	r.Write("x.log", "1")
	tb.sel = "changes"
	h.tt.Frame()
	h.click("More")
	if err := h.tt.ChooseMenuItem("Clean Untracked Files…"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the list", h.shows("Delete 1 Item"))
	h.click("Include ignored files")
	h.until("the list with ignored", h.shows("Delete 2 Items"))
	h.click("Delete 2 Items")
	h.until("cleaned", func() bool {
		_, e1 := os.Stat(filepath.Join(r.Dir, "x.log"))
		_, e2 := os.Stat(filepath.Join(r.Dir, "new.txt"))
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	})
}

// Cherry-pick and revert from history; bisect with the bar; save a
// commit as a patch and apply it.
func TestCommitOps(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	dir := t.TempDir()
	h.app.SaveFile = func(_, name string) (string, error) { return filepath.Join(dir, name), nil }
	var pick string
	h.app.PickFile = func(string) (string, error) { return pick, nil }
	r.Git("switch", "-q", "-c", "topic")
	r.Write("t", "1")
	r.Commit("topic work")
	r.Git("switch", "-q", "main")
	for i := range 6 {
		r.Write("n", strings.Repeat("x", i+1))
		r.Commit("step " + string(rune('a'+i)))
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("topic work"))

	h.menu("topic work", "Cherry-Pick Commit onto main")
	h.until("picked", func() bool { return strings.HasPrefix(r.Git("log", "-1", "--format=%s"), "topic work") })
	h.idle(tb, "the pick")
	h.until("the history again", func() bool { return len(h.app.current().hist.labels) > 0 && h.tt.HasText("step f") })
	h.menu("step f", "Revert Commit")
	h.idle(tb, "the revert")
	if l := tb.s.Log; l[len(l)-1].Err != nil || h.app.notice != "" {
		t.Fatalf("%s: %v; notice %q; output %q", l[len(l)-1].Name, l[len(l)-1].Err, h.app.notice, l[len(l)-1].Output)
	}
	h.until("reverted", func() bool { return strings.HasPrefix(r.Git("log", "-1", "--format=%s"), "Revert \"step f\"") })
	h.idle(tb, "the revert")

	h.menu("step b", "Save Commit as Patch…")
	var patch string
	h.until("the patch", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "*.patch"))
		if len(m) == 1 {
			patch = m[0]
		}
		return patch != ""
	})
	b, _ := os.ReadFile(patch)
	if !strings.Contains(string(b), "Subject: [PATCH] step b") {
		t.Fatalf("patch:\n%s", b)
	}

	// Bisect from the menu, then the bar.
	h.menu("step e", "Bisect", "Mark Bad")
	h.idle(tb, "the mark")
	h.until("the bar", h.shows("mark a good commit"))
	h.menu("step a", "Bisect", "Mark Good")
	h.until("bisecting", h.shows("left to test"))
	for range 6 {
		if h.tt.HasText("First bad commit") {
			break
		}
		h.click("Bad")
		h.idle(tb, "the mark")
		h.until("the next step", func() bool { return !tb.bisect.loading })
		h.tt.Frame()
	}
	h.until("the result", h.shows("First bad commit"))
	h.click("End Bisect")
	h.until("ended", func() bool { _, err := os.Stat(filepath.Join(r.GitDir(), "BISECT_START")); return os.IsNotExist(err) })
	h.idle(tb, "the reset")

	// The saved patch goes back on as a commit.
	stepA := strings.TrimSpace(r.Git("log", "-1", "--format=%H", "--grep=^step a$"))
	r.Git("reset", "-q", "--hard", stepA)
	h.idle(tb, "the reset shown")
	pick = patch
	tb.sel = "changes"
	h.tt.Frame()
	h.click("More")
	if err := h.tt.ChooseMenuItem("Apply Patch File…"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", h.shows("as commits?"))
	h.click("Apply")
	h.until("applied", func() bool { return strings.HasPrefix(r.Git("log", "-1", "--format=%s"), "step b") })
}
