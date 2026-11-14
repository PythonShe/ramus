package ui

import (
	"strings"
	"testing"
)

// A branch stacked on another says so, and its menu pushes the stack.
func TestStackInBranches(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("config", "receive.denyCurrentBranch", "ignore")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("reset", "-q", "--hard", "origin/main")
	r.Git("remote", "set-head", "origin", "main")
	for _, b := range []string{"bottom", "top"} {
		r.Git("checkout", "-q", "-b", b)
		r.Write(b, b)
		r.Commit(b)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	h.click("Branches and Tags")
	h.until("the stack", h.shows("on bottom"))
	h.menu("top", "Push Stack of 2 Branches")
	h.until("the push", func() bool {
		out, _ := up.Run(nil, "rev-parse", "-q", "--verify", "refs/heads/top")
		return strings.TrimSpace(out) == strings.TrimSpace(r.Git("rev-parse", "top"))
	})
	if out, _ := up.Run(nil, "rev-parse", "-q", "--verify", "refs/heads/bottom"); strings.TrimSpace(out) == "" {
		t.Fatal("the bottom branch was not pushed")
	}
}
