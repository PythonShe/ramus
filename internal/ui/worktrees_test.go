package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Worktrees made from a branch and a remote branch, locked, removed with
// their branch, and pruned once their folder is gone; git worktree list
// is the oracle.
func TestWorktreeActions(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	r.Git("branch", "topic")
	r.Git("remote", "add", "origin", "https://example.invalid/r.git")
	r.Git("update-ref", "refs/remotes/origin/feature", "HEAD")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	list := func() string { return r.Git("worktree", "list", "--porcelain") }
	// as is a path as git lists it: with forward slashes on Windows too.
	as := filepath.ToSlash

	// A local branch, opened in a tab.
	h.until("the branches", func() bool { return tb.refs.info != nil })
	h.menu("topic", "New Worktree…")
	h.until("the dialog", h.shows("New Worktree for topic"))
	if want := filepath.Join(filepath.Dir(r.Dir), filepath.Base(r.Dir)+"-topic"); h.app.worktreeUI.path != want {
		t.Fatalf("suggested %q, want %q", h.app.worktreeUI.path, want)
	}
	topic := filepath.Join(base, "topic")
	h.app.worktreeUI.path = topic
	h.tt.Frame()
	h.click("Create")
	h.until("its tab", func() bool { return len(h.app.tabs) == 2 })
	if !strings.Contains(list(), "worktree "+as(topic)+"\n") {
		t.Fatalf("list\n%s", list())
	}
	h.app.Close(1)
	h.app.active = 0
	label := "topic  topic"
	h.until("the worktree in the sidebar", h.shows(label))

	// Locked with a reason, then unlocked.
	h.menu(label, "Lock…")
	h.until("the lock dialog", h.shows("Lock worktree topic"))
	h.tt.Type("on a stick")
	h.click("Lock")
	h.until("locked", func() bool { return strings.Contains(list(), "locked on a stick") && h.tt.HasText(label+"  locked") })
	h.menu(label+"  locked", "Unlock")
	h.until("unlocked", func() bool { return !strings.Contains(list(), "locked") && hasExact(h, label) })

	// Removed with its branch.
	h.menu(label, "Remove…")
	h.until("the removal", h.shows("Remove worktree topic?"))
	h.click("Also delete branch topic")
	h.click("Remove")
	h.until("removed", func() bool {
		_, err := r.Run(nil, "rev-parse", "--verify", "-q", "refs/heads/topic")
		return !strings.Contains(list(), as(topic)) && err != nil
	})
	if _, err := os.Stat(topic); !os.IsNotExist(err) {
		t.Fatalf("folder left: %v", err)
	}

	// A remote branch: a new branch tracking it, no tab.
	h.until("the remote branch", h.shows("origin/feature"))
	h.menu("origin/feature", "New Worktree…")
	h.until("the remote dialog", h.shows("New Worktree for origin/feature"))
	if h.app.worktreeUI.name != "feature" {
		t.Fatalf("branch name %q", h.app.worktreeUI.name)
	}
	feature := filepath.Join(base, "feature")
	h.app.worktreeUI.path = feature
	h.tt.Frame()
	h.click("Open in a new tab")
	h.click("Create")
	h.until("the feature worktree", func() bool { return strings.Contains(list(), "worktree "+as(feature)+"\n") })
	if up := strings.TrimSpace(r.Git("config", "branch.feature.merge")); up != "refs/heads/feature" || len(h.app.tabs) != 1 {
		t.Fatalf("upstream %q, %d tabs", up, len(h.app.tabs))
	}

	t.Cleanup(func() {
		if t.Failed() {
			st := tb.s.State
			t.Logf("worktrees %+v; refresh error %v; watch error %v", st.Worktrees, st.RefreshErr, st.WatchErr)
			for _, e := range tb.s.Log {
				t.Logf("log: %s: %v %q", e.Name, e.Err, e.Output)
			}
		}
	})
	h.until("the feature worktree in the sidebar", h.shows("feature  feature"))

	// Its folder gone: choosing it offers to prune, not a failed open.
	if err := os.RemoveAll(feature); err != nil {
		t.Fatal(err)
	}
	h.app.Reload()
	missing := "feature  feature  folder missing"
	h.until("the missing folder", h.shows(missing))
	h.click(missing)
	h.until("the question", h.shows("Worktree feature is missing"))
	if len(h.app.tabs) != 1 {
		t.Fatal("a missing worktree was opened")
	}
	h.click("Prune")
	h.until("pruned", func() bool { return !strings.Contains(list(), as(feature)) && !h.tt.HasText(missing) })
}
