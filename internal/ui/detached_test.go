package ui

import (
	"encoding/hex"
	"testing"
)

// A commit made on a detached HEAD moves no ref; history shows it all
// the same.
func TestDetachedCommitInHistory(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "a\n")
	r.Commit("add a")
	r.Git("checkout", "-q", "--detach")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.click("History")
	h.until("history", h.shows("add a"))
	r.Write("b.txt", "b\n")
	id := r.Commit("on detached")
	h.until("the state", func() bool { return hex.EncodeToString(tb.s.State.Head.OID) == id })
	h.until("the commit in history", h.shows("on detached"))
}
