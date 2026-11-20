package ui

import (
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
	"ramus/internal/git/write"

	"ramus/locales"
)

// An older commit is reworded, fixed up with what is staged, and dropped
// from its menu in the history; a tag is made there too.
func TestRewriteFromHistory(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, f := range []string{"a", "b", "c"} {
		r.Write(f+".txt", f+"\n")
		r.Commit("add " + f)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("history", h.shows("add b"))
	// Asked while the rewrite runs: with a rebase half done, rev may name
	// nothing yet, which is not a failure.
	subject := func(rev string) string {
		out, err := r.Run(nil, "log", "-1", "--format=%s", rev)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	// fresh waits for history read for the refs as they are now: rows of
	// commits a rewrite replaced name commits on no branch.
	fresh := func(what string) {
		h.idle(tb, what)
		h.until(what, func() bool { hs := tb.s.History; return hs.Done && hs.For == tb.s.State.Refs })
	}

	h.menu("add b", "Edit Commit", "Reword…")
	h.until("the dialog", func() bool { return h.app.message.open && h.app.message.message == "add b" })
	h.app.message.message = "add b, reworded"
	h.click("Reword")
	h.until("reworded", func() bool { return subject("HEAD~1") == "add b, reworded" })
	fresh("the reword shown")

	r.Write("a.txt", "a fixed\n")
	r.Git("add", "a.txt")
	h.until("staged", func() bool { return hasStaged(tb.s.State.Status) })
	h.until("history again", h.shows("add a"))
	h.menu("add a", "Edit Commit", "Fix Up with Staged Changes")
	h.until("fixed up", func() bool {
		out, err := r.Run(nil, "show", "HEAD~2:a.txt")
		return err == nil && out == "a fixed\n"
	})
	fresh("the fixup shown")

	h.until("history again", h.shows("add b, reworded"))
	h.menu("add b, reworded", "Edit Commit", "Drop Commit…")
	h.until("the question", func() bool {
		if h.app.notice != "" {
			t.Fatal(h.app.notice)
		}
		return h.app.confirm.open
	})
	h.click("Drop")
	h.until("dropped", func() bool {
		if l := tb.s.Log; len(l) > 0 && l[len(l)-1].Err != nil {
			t.Fatalf("%s: %v", l[len(l)-1].Name, l[len(l)-1].Err)
		}
		if h.app.notice != "" {
			t.Fatal(h.app.notice)
		}
		return subject("HEAD~1") == "add a"
	})
	fresh("the drop shown")

	h.until("history again", h.shows("add c"))
	h.menu("add c", "Create Tag Here…")
	h.until("the tag dialog", h.shows("Create Tag at"))
}

// Rewording a commit already pushed offers a force push with lease, which
// asks first, and then sends the rewritten branch.
func TestForcePushOffer(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("config", "receive.denyCurrentBranch", "ignore")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("reset", "-q", "--hard", "origin/main")
	r.Git("branch", "-q", "--set-upstream-to", "origin/main")
	r.Write("a.txt", "a\n")
	r.Commit("add a")
	r.Git("push", "-q", "origin", "main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.click("History")
	h.until("history", h.shows("add a"))
	h.menu("add a", "Edit Commit", "Reword…")
	h.until("the dialog", func() bool { return h.app.message.open })
	h.app.message.message = "add a, reworded"
	h.click("Reword")
	h.until("the offer", h.shows("was rewritten after it was pushed to origin/main"))
	h.click("Force Push with Lease")
	h.until("the question", h.shows("Overwrite origin/main?"))
	if out, _ := up.Run(nil, "log", "-1", "--format=%s", "main"); strings.TrimSpace(out) != "add a" {
		t.Fatalf("pushed before asked: %q", out)
	}
	h.click("Force Push")
	h.until("the push", func() bool {
		out, _ := up.Run(nil, "log", "-1", "--format=%s", "main")
		return strings.TrimSpace(out) == "add a, reworded"
	})
}

// The force push offered for a rewritten branch, confirmed once another
// branch is checked out, pushes nothing: git would push that one, to
// the upstream the user was asked about.
func TestForcePushOfferBranchChanged(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("config", "receive.denyCurrentBranch", "ignore")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("reset", "-q", "--hard", "origin/main")
	r.Git("branch", "-q", "--set-upstream-to", "origin/main")
	// Each branch pushes to its upstream, whatever its name.
	r.Git("config", "push.default", "upstream")
	r.Write("a.txt", "a\n")
	r.Commit("add a")
	r.Git("push", "-q", "origin", "main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("history", h.shows("add a"))
	h.menu("add a", "Edit Commit", "Reword…")
	h.until("the dialog", func() bool { return h.app.message.open })
	h.app.message.message = "add a, reworded"
	h.click("Reword")
	h.until("the offer", h.shows("was rewritten after it was pushed to origin/main"))
	h.click("Force Push with Lease")
	h.until("the question", h.shows("Overwrite origin/main?"))

	r.Git("switch", "-q", "-c", "other", "HEAD~1")
	r.Git("branch", "-q", "--set-upstream-to", "origin/main")
	r.Write("o.txt", "o\n")
	r.Commit("other work")
	h.until("the switch", func() bool { return tb.s.State.Head.Target == "refs/heads/other" })
	h.click("Force Push")
	h.until("why nothing was pushed", func() bool { return h.app.notice != "" })
	if !strings.Contains(h.app.notice, "main is no longer checked out") {
		t.Fatalf("notice %q", h.app.notice)
	}
	h.idle(tb, "no push")
	if out, _ := up.Run(nil, "log", "-1", "--format=%s", "main"); strings.TrimSpace(out) != "add a" {
		t.Fatalf("pushed %q", out)
	}
}

// The history editor replays the commits from one on as its rows say:
// a reword chosen from a row's menu, rows moved, a fix up; a branch at a
// commit moves with it.
func TestEditHistoryDialog(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, f := range []string{"a", "b", "c", "d"} {
		r.Write(f+".txt", f+"\n")
		r.Commit("add " + f)
	}
	r.Git("branch", "side", "HEAD~2")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("history", h.shows("add b"))

	h.menu("add b", "Edit Commit", "Edit History from Here…")
	he := &h.app.history
	h.until("the editor", func() bool {
		if h.app.notice != "" {
			t.Fatal(h.app.notice)
		}
		return he.open && len(he.rows) == 3
	})
	if !hasExact(h, "side") || he.rows[0].Subject != "add b" {
		t.Fatalf("rows: %q", h.tt.Texts())
	}
	// Shown newest first, as History shows them: add d is the first row.
	if texts := h.tt.Texts(); slices.Index(texts, "Pick, add d") > slices.Index(texts, "Pick, add b") {
		t.Fatalf("not newest first: %q", texts)
	}
	h.click("Pick")
	if err := h.tt.ChooseMenuItem("Reword"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the message", func() bool { return he.rows[2].message == "add d" })
	he.rows[2].message = "add d, reworded"
	he.reorder([]int{0}, 2) // d, shown first, dragged below c: before it, in git's order
	if he.rows[1].Subject != "add d" {
		t.Fatalf("moved to %q", he.rows[1].Subject)
	}
	he.rows[2].action = write.DoFixup
	h.click("Rewrite 3 Commits")
	h.until("the rewrite", func() bool {
		if l := tb.s.Log; len(l) > 0 && l[len(l)-1].Err != nil {
			t.Fatalf("%s: %v", l[len(l)-1].Name, l[len(l)-1].Err)
		}
		return strings.TrimSpace(r.Git("log", "--format=%s", "main")) == "add d, reworded\nadd b\nadd a\none"
	})
	h.idle(tb, "the rewrite done")
	if r.Git("show", "HEAD:c.txt") != "c\n" || strings.TrimSpace(r.Git("log", "-1", "--format=%s", "side")) != "add b" {
		t.Fatal("c is not folded into d, or side moved")
	}
	if he.open {
		t.Fatal("the editor is still open")
	}
}

// Commits picked together are squashed or dropped through the history
// editor, filled in with the plan; a commit dragged onto another opens it
// with the one squashed into the other.
func TestRewriteManyAndDragSquash(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, f := range []string{"a", "b", "c", "d"} {
		r.Write(f+".txt", f+"\n")
		r.Commit("add " + f)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("history", h.shows("add d"))
	he := &h.app.history
	// main, not HEAD: during the rebase HEAD is the commits replayed so far.
	log := func() string { return strings.TrimSpace(r.Git("log", "--format=%s", "main")) }
	pick := func(subjects ...string) {
		t.Helper()
		hs := tb.s.History
		tb.hist.picked.Clear()
		for _, s := range subjects {
			id := strings.TrimSpace(r.Git("log", "-1", "--format=%H", "--grep=^"+s+"$"))
			for k := 0; k < hs.Snap.Len(); k++ {
				if c := hs.OID(hs.Snap.Commit(k)); hex.EncodeToString(c) == id {
					tb.hist.picked.Add(string(c))
				}
			}
		}
		if tb.hist.picked.Len() != len(subjects) {
			t.Fatalf("picked %d of %q", tb.hist.picked.Len(), subjects)
		}
	}
	rewrite := func(want string) {
		t.Helper()
		h.until("the editor", func() bool {
			if h.app.notice != "" {
				t.Fatal(h.app.notice)
			}
			return he.open
		})
		h.click(locales.RewriteCommits(len(he.rows)))
		h.until(want, func() bool {
			if l := tb.s.Log; len(l) > 0 && l[len(l)-1].Err != nil {
				t.Fatalf("%s: %v", l[len(l)-1].Name, l[len(l)-1].Err)
			}
			return log() == want
		})
		h.idle(tb, "the rewrite done")
		h.until("fresh history", func() bool { hs := tb.s.History; return hs.Done && hs.For == tb.s.State.Refs })
	}

	pick("add b", "add d")
	h.menu("add d", "Squash 2 Commits…")
	rewrite("add c\nadd b\nadd a\none")
	if body := r.Git("log", "-1", "--format=%B", "HEAD~1"); body != "add b\n\nadd d\n\n" || r.Git("show", "HEAD~1:d.txt") != "d\n" {
		t.Fatalf("squashed: %q", body)
	}

	h.until("history again", h.shows("add c"))
	pick("add a", "add c")
	h.menu("add c", "Drop 2 Commits…")
	rewrite("add b\none")

	// Drag "one" onto "add b": "one" is the oldest, so the edit starts
	// there, and it is folded into "add b".
	h.until("history again", func() bool { return hasExact(h, "add b") && hasExact(h, "one") })
	tb.hist.picked.Clear()
	from, ok1 := h.tt.Find("one")
	to, ok2 := h.tt.Find("add b")
	if !ok1 || !ok2 {
		t.Fatalf("rows: %q", h.tt.Texts())
	}
	h.tt.Press(from.X+4, from.Y+from.H/2)
	for k := 1; k <= 4; k++ {
		f := float32(k) / 4
		h.tt.Move(from.X+4, from.Y+from.H/2+(to.Y-from.Y)*f)
		h.tt.Frame()
	}
	h.tt.Release(to.X+4, to.Y+to.H/2)
	h.tt.Frame()
	h.until("the editor", func() bool { return he.open })
	if len(he.rows) != 2 || he.rows[1].action != write.DoSquash || he.rows[1].Subject != "one" {
		t.Fatalf("plan: %+v", he.rows)
	}
	rewrite("add b")
}

// Squash into a commit is offered on every git Ramus runs on, at its
// place in a commit's Edit Commit menu: before git history fixup it is
// done through a rebase.
func TestSquashIntoOnEveryGit(t *testing.T) {
	for _, v := range []gitexe.Version{{}, {Major: 2, Minor: 50, Raw: "2.50.0"}} {
		name := v.Raw
		if name == "" {
			name = "git on PATH"
		}
		t.Run(name, func(t *testing.T) {
			g := testrun.Git(t)
			if v.Raw != "" {
				g.Version = v
			}
			h := newHarnessWith(t, run.New(g, gittest.Env(), 8))
			r := h.repoWith()
			for _, f := range []string{"a", "b"} {
				r.Write(f+".txt", f+"\n")
				r.Commit("add " + f)
			}
			r.Write("a.txt", "a squashed\n")
			r.Git("add", "a.txt")
			h.trust.Set(r.GitDir(), true)
			h.app.Open(r.Dir)
			tb := h.app.current()
			h.until("staged", func() bool { return hasStaged(tb.s.State.Status) })
			h.click("History")
			h.until("history", h.shows("add a"))
			h.menu("add a", "Edit Commit", "Squash Staged Changes into This…")
			h.until("the dialog", func() bool { return h.app.message.open && h.app.message.message == "add a" })
			h.app.message.message = "add a, squashed"
			h.click("Squash")
			h.until("squashed", func() bool {
				if l := tb.s.Log; len(l) > 0 && l[len(l)-1].Err != nil {
					t.Fatalf("%s: %v", l[len(l)-1].Name, l[len(l)-1].Err)
				}
				return strings.TrimSpace(r.Git("log", "--format=%s", "main")) == "add b\nadd a, squashed\none"
			})
			h.idle(tb, "the squash done")
			if r.Git("show", "HEAD~1:a.txt") != "a squashed\n" {
				t.Fatal("the staged change is not in the commit")
			}
		})
	}
}
