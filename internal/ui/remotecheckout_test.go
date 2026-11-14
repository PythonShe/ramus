package ui

import (
	"path/filepath"
	"strings"
	"testing"
)

// A remote branch checks out as a new branch tracking it; over a local
// branch of its name, the dialog says how they stand and offers what
// fits; a branch checked out in another worktree is explained.
func TestCheckOutRemote(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	one := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Write("b", "2")
	two := r.Commit("two")
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	r.Git("remote", "add", "origin", "https://example.invalid/r.git")
	r.Git("update-ref", "refs/remotes/origin/fresh", two)
	r.Git("update-ref", "refs/remotes/origin/feature", two)
	r.Git("update-ref", "refs/remotes/origin/side", two)
	// feature tracks origin/feature, one commit behind it.
	r.Git("branch", "--no-track", "feature", one)
	r.Git("branch", "--set-upstream-to=origin/feature", "feature")
	// side is checked out in another worktree.
	other := filepath.Join(tmp, "side")
	r.Git("worktree", "add", "-q", "-b", "side", other, one)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the branches", func() bool { return tb.refs.info != nil && h.tt.HasText("origin/fresh") })
	t.Cleanup(func() {
		if t.Failed() {
			for _, e := range tb.s.Log {
				t.Logf("log: %s: %v %q", e.Name, e.Err, e.Output)
			}
			t.Logf("notice %q", h.app.notice)
		}
	})
	head := func() string { return strings.TrimSpace(r.Git("symbolic-ref", "--short", "HEAD")) }
	// on waits for the state to show HEAD on branch b: a write decided on
	// an older HEAD is refused.
	on := func(b string) {
		h.until("HEAD on "+b, func() bool { return head() == b && tb.s.State.Head.Target == "refs/heads/"+b })
	}

	h.menu("origin/fresh", "Check Out")
	on("fresh")

	h.menu("origin/feature", "Check Out")
	h.until("the dialog", h.shows("feature is 1 commit behind origin/feature."))
	h.click("Fast-Forward and Switch")
	on("feature")
	if strings.TrimSpace(r.Git("rev-parse", "feature")) != two {
		t.Fatal("feature not fast-forwarded")
	}

	// Another name for the same remote branch.
	h.until("the branches again", func() bool { return tb.refs.info != nil && !tb.refs.loading })
	h.menu("origin/feature", "Check Out")
	h.until("the dialog again", h.shows("feature is at the same commit as origin/feature."))
	h.app.remoteUI.other = "feature-2"
	h.tt.Frame()
	h.click("Check Out as New Branch")
	on("feature-2")

	h.menu("origin/side", "Check Out")
	h.until("the explanation", h.shows("Branch side is checked out in another worktree"))
	h.click("Open side")
	h.until("its tab", func() bool { return len(h.app.tabs) == 2 })
}
