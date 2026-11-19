package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/session"
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

// dialogShape is where a dialog's elements are, the dialog's own box
// first: with a dialog up, the rest of the window is out of the tree.
type dialogShape []ui.AccessInfo

func (h *harness) dialogShape() dialogShape { return dialogShape(h.tt.AccessAll()) }

// differs says how s and laid out differ, "" when nothing moved: every
// element where it was and as large, but a text, whose words change,
// which starts where it did and stays inside the box laid out.
func (s dialogShape) differs(laid dialogShape) string {
	if len(s) != len(laid) {
		return fmt.Sprintf("%d elements, laid out %d", len(s), len(laid))
	}
	for i, e := range s {
		l := laid[i]
		b, lb := e.Bounds, l.Bounds
		switch {
		case e.Role != l.Role:
			return fmt.Sprintf("element %d (%q) is a %v, laid out a %v (%q)", i, e.Name, e.Role, l.Role, l.Name)
		case e.Role == ui.RoleText && (b.X != lb.X || b.Y != lb.Y || b.W > lb.W || b.H > lb.H):
			return fmt.Sprintf("text %q at %v, laid out at %v (%q)", e.Name, b, lb, l.Name)
		case e.Role != ui.RoleText && b != lb:
			return fmt.Sprintf("%q at %v, laid out at %v", e.Name, b, lb)
		}
	}
	return ""
}

// A remote branch checked out before the branches' details are read
// opens its dialog at once, laid out as it always was then, for a branch
// that does not track the remote one, and keeps that shape as the
// details land, whatever they say: they come in its places, with no
// reset offered before they are read, nor one git or its guard would
// refuse after.
func TestCheckOutRemoteBeforeDetails(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	one := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Write("b", "2")
	two := r.Commit("two")
	r.Git("checkout", "-q", "-b", "aside", one)
	r.Write("c", "3")
	aside := r.Commit("aside")
	r.Git("checkout", "-q", "main")
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	r.Git("remote", "add", "origin", "https://example.invalid/r.git")
	long := "topic/" + strings.Repeat("a-long-branch-name-", 8) + "end"
	branch := func(name, at, upstream string) {
		r.Git("update-ref", "refs/remotes/origin/"+name, upstream)
		r.Git("branch", "--no-track", name, at)
		r.Git("branch", "--set-upstream-to=origin/"+name, name)
	}
	branch("ahead", two, one)
	branch("behind", one, two)
	branch("diverged", aside, two)
	branch("same", two, two)
	branch(long, one, two)
	branch("here", one, two)
	r.Git("update-ref", "refs/remotes/origin/plain", two)
	r.Git("branch", "--no-track", "plain", one)
	r.Git("update-ref", "refs/remotes/origin/side", two)
	r.Git("worktree", "add", "-q", "-b", "side", filepath.Join(tmp, "side"), one)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	read := func() bool { return tb.refs.info != nil && !tb.refs.loading }
	h.until("the branches", read)

	for _, c := range []struct {
		name, says string
		reset      bool
	}{
		{"ahead", "ahead is 1 commit ahead of origin/ahead, not pushed.", false},
		{"behind", "behind is 1 commit behind origin/behind.", true},
		{"diverged", "diverged and origin/diverged have diverged: 1 commit ahead, 1 commit behind.", true},
		{"same", "same is at the same commit as origin/same.", false},
		{"plain", "plain does not track origin/plain; resetting it drops what it has that origin/plain does not.", true},
		{long, long + " is 1 commit behind origin/" + long + ".", true},
		// The folder does not fit in the lines laid out.
		{"side", "Branch side is checked out in another worktree", false},
		{"here", "here is 1 commit behind origin/here.", true},
	} {
		func() {
			fatalf := func(f string, v ...any) { t.Helper(); t.Fatalf("%s: "+f, append([]any{c.name}, v...)...) }
			if c.name == "here" {
				r.Git("switch", "-q", "here")
				h.until("HEAD on here", func() bool { return tb.s.State.Head.Target == "refs/heads/here" })
			}
			h.until("the branches read", read)
			tb.refs.info, tb.refs.readFor = nil, nil // as right after the repository opened
			h.app.checkOutRemote(tb, "refs/remotes/origin/"+c.name)
			h.tt.Frame()
			d := h.app.remoteUI
			if !d.open || !d.unread {
				fatalf("no dialog while the details are read: %q", h.tt.Texts())
			}
			reading := h.dialogShape()
			reset, ok := h.tt.AccessByRole(ui.RoleButton, "Reset to origin/"+c.name)
			if !ok || !reset.Disabled || !h.tt.HasText("Reading how "+c.name+" stands against origin/"+c.name+"…") {
				fatalf("while read, the dialog shows %q", h.tt.Texts())
			}
			w := tb.s.Writes
			h.click("Reset to origin/" + c.name)
			if !h.app.remoteUI.open || tb.s.Writes != w {
				fatalf("the reset ran before the branch was read")
			}

			h.until("the details", func() bool { return !h.app.remoteUI.unread })
			h.tt.Frame()
			landed := h.dialogShape()
			if !h.tt.HasText(c.says) {
				t.Errorf("%s: read, the dialog shows %q", c.name, h.tt.Texts())
			}
			reset, _ = h.tt.AccessByRole(ui.RoleButton, "Reset to origin/"+c.name)
			if reset.Disabled == c.reset {
				t.Errorf("%s: read, Reset disabled %v", c.name, reset.Disabled)
			}
			sw, ok := h.tt.AccessByRole(ui.RoleButton, "Switch to "+c.name)
			if ok != (c.name != "here") || ok && sw.Disabled != (c.name == "side") {
				t.Errorf("%s: read, Switch shown %v, disabled %v", c.name, ok, sw.Disabled)
			}

			// As the dialog opened before the details were read was laid
			// out, and stayed, before it waited for them: the branch as one
			// of the name, nothing known of it.
			h.app.remoteUI = remoteCheckout{open: true, t: tb, remote: d.remote, local: session.BranchInfo{Name: d.local.Name}, other: d.other}
			h.tt.Frame()
			laid := h.dialogShape()
			if !h.tt.HasText(c.name + " does not track origin/" + c.name) {
				fatalf("laid out, the dialog shows %q", h.tt.Texts())
			}
			if diff := reading.differs(laid); diff != "" {
				t.Errorf("%s while read: %s", c.name, diff)
			}
			if diff := landed.differs(laid); diff != "" {
				t.Errorf("%s read: %s", c.name, diff)
			}
			h.click("Cancel")
		}()
	}

	// Read, a reset of a branch behind is a fast-forward.
	h.until("the branches read", read)
	tb.refs.info, tb.refs.readFor = nil, nil
	h.app.checkOutRemote(tb, "refs/remotes/origin/behind")
	h.until("the details", func() bool { return !h.app.remoteUI.unread })
	h.click("Reset to origin/behind")
	h.until("behind fast-forwarded", func() bool { return strings.TrimSpace(r.Git("rev-parse", "behind")) == two })
}
