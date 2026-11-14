package ui

import (
	"strings"
	"testing"
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
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.click("History")
	h.until("history", h.shows("add b"))
	subject := func(rev string) string { return strings.TrimSpace(r.Git("log", "-1", "--format=%s", rev)) }
	// fresh waits for history read for the refs as they are now: rows of
	// commits a rewrite replaced name commits on no branch.
	fresh := func(what string) {
		h.idle(tb, what)
		h.until(what, func() bool { hs := tb.s.History; return hs.Done && hs.For == tb.s.State.Refs })
	}

	h.menu("add b", "Reword…")
	h.until("the dialog", func() bool { return h.app.message.open && h.app.message.message == "add b" })
	h.app.message.message = "add b, reworded"
	h.click("Reword")
	h.until("reworded", func() bool { return subject("HEAD~1") == "add b, reworded" })
	fresh("the reword shown")

	r.Write("a.txt", "a fixed\n")
	r.Git("add", "a.txt")
	h.until("staged", func() bool { return hasStaged(tb.s.State.Status) })
	h.until("history again", h.shows("add a"))
	h.menu("add a", "Fix Up with Staged Changes")
	h.until("fixed up", func() bool { return r.Git("show", "HEAD~2:a.txt") == "a fixed\n" })
	fresh("the fixup shown")

	h.until("history again", h.shows("add b, reworded"))
	h.menu("add b, reworded", "Drop Commit…")
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
// sends the rewritten branch.
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
	h.until("the repository", h.shows("No changes"))
	h.click("History")
	h.until("history", h.shows("add a"))
	h.menu("add a", "Reword…")
	h.until("the dialog", func() bool { return h.app.message.open })
	h.app.message.message = "add a, reworded"
	h.click("Reword")
	h.until("the offer", h.shows("was rewritten after it was pushed to origin/main"))
	h.click("Force Push with Lease")
	h.until("the push", func() bool {
		out, _ := up.Run(nil, "log", "-1", "--format=%s", "main")
		return strings.TrimSpace(out) == "add a, reworded"
	})
}
