package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
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

	h.menu("origin/fresh", "Switch")
	on("fresh")

	h.menu("origin/feature", "Switch")
	h.until("the dialog", h.shows("feature is 1 commit behind origin/feature."))
	h.click("Fast-Forward and Switch")
	on("feature")
	if strings.TrimSpace(r.Git("rev-parse", "feature")) != two {
		t.Fatal("feature not fast-forwarded")
	}

	// Another name for the same remote branch.
	h.until("the branches again", func() bool { return tb.refs.info != nil && !tb.refs.loading })
	h.menu("origin/feature", "Switch")
	h.until("the dialog again", h.shows("feature is at the same commit as origin/feature."))
	h.app.remoteUI.other = "feature-2"
	h.tt.Frame()
	h.click("Check Out as New Branch")
	on("feature-2")

	h.menu("origin/side", "Switch")
	h.until("the explanation", h.shows("Branch side is checked out in another worktree"))
	h.click("Open side")
	h.until("its tab", func() bool { return len(h.app.tabs) == 2 })
}

// A remote branch checked out before the branches' details are read
// waits for them to say how the local branch of its name stands, and to
// offer what fits: never a reset its guard would refuse.
func TestCheckOutRemoteBeforeDetails(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	one := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Write("b", "2")
	two := r.Commit("two")
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	r.Git("remote", "add", "origin", "https://example.invalid/r.git")
	r.Git("update-ref", "refs/remotes/origin/feature", two)
	r.Git("update-ref", "refs/remotes/origin/side", two)
	r.Git("update-ref", "refs/remotes/origin/plain", two)
	r.Git("branch", "--no-track", "feature", one)
	r.Git("branch", "--set-upstream-to=origin/feature", "feature")
	r.Git("branch", "--no-track", "plain", one)
	r.Git("worktree", "add", "-q", "-b", "side", filepath.Join(tmp, "side"), one)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the branches", func() bool { return tb.refs.info != nil && !tb.refs.loading })
	unread := func() {
		// As right after the repository opened.
		tb.refs.info, tb.refs.readFor = nil, nil
	}

	// While it reads, the dialog has the shape of a reset, the most
	// outcomes', with the reset disabled: it does not grow as they land.
	reading := func(name string) {
		t.Helper()
		b, ok := h.tt.AccessByRole(ui.RoleButton, "Reset to origin/"+name)
		if !ok || !b.Disabled || !h.tt.HasText("A reset backs up "+name+" first; Undo brings it back.") {
			t.Fatalf("reading %s, the dialog shows %q", name, h.tt.Texts())
		}
		if h.tt.HasText(name + " does not track origin/" + name + "; resetting it drops what it has that origin/" + name + " does not.") {
			t.Fatalf("%s said not to track before it was read", name)
		}
		w := tb.s.Writes
		h.click("Reset to origin/" + name)
		if !h.app.remoteUI.open || tb.s.Writes != w {
			t.Fatalf("the reset ran before %s was read", name)
		}
	}
	unread()
	h.app.checkOutRemote(tb, "refs/remotes/origin/feature")
	h.tt.Frame()
	reading("feature")
	h.until("the dialog", h.shows("feature is 1 commit behind origin/feature."))
	h.click("Fast-Forward and Switch")
	h.until("feature fast-forwarded", func() bool { return strings.TrimSpace(r.Git("rev-parse", "feature")) == two })

	// One that does not track it keeps the shape it had while read.
	h.until("the branches again", func() bool { return tb.refs.info != nil && !tb.refs.loading })
	unread()
	h.app.checkOutRemote(tb, "refs/remotes/origin/plain")
	h.tt.Frame()
	reading("plain")
	h.until("the dialog", h.shows("plain does not track origin/plain; resetting it drops what it has that origin/plain does not."))
	if b, ok := h.tt.AccessByRole(ui.RoleButton, "Reset to origin/plain"); !ok || b.Disabled || !h.tt.HasText("A reset backs up plain first; Undo brings it back.") {
		t.Fatalf("read, the dialog shows %q", h.tt.Texts())
	}
	h.click("Cancel")

	h.until("the branches again", func() bool { return tb.refs.info != nil && !tb.refs.loading })
	unread()
	h.app.checkOutRemote(tb, "refs/remotes/origin/side")
	h.until("the explanation", h.shows("Branch side is checked out in another worktree"))
}
