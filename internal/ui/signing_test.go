package ui

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// A commit signed from the commit box, its signature checked in its
// detail, and a signed tag checked from its menu; git verify-commit is
// the oracle for the commit.
func TestSigning(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	gittest.SSHSigning(t, r)
	r.Git("tag", "-s", "-m", "release", "v1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the commit box", h.shows("Sign"))
	tb := h.app.current()
	cs := &tb.commit
	h.until("the setup", func() bool { return cs.setup != nil })
	if cs.sign {
		t.Fatal("signing on with commit.gpgSign unset")
	}
	r.Write("a.txt", "a")
	r.Git("add", "a.txt")
	h.until("staged", h.shows("Staged (1)"))
	h.click("Sign")
	cs.subject = "signed here"
	h.click("Commit to main")
	h.until("the commit", func() bool { return strings.TrimSpace(r.Git("log", "-1", "--format=%s")) == "signed here" })
	if out, err := r.Run(nil, "verify-commit", "HEAD"); err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}

	h.click("History")
	h.until("the history", h.shows("signed here"))
	h.click("signed here")
	h.until("its signature", h.shows("Good signature, signer test@example.com"))

	h.click("Branches and Tags")
	h.click("Tags")
	h.until("the tag", h.shows("signed"))
	h.menu("v1", "Check Signature")
	h.until("the tag's signature", h.shows("Good signature, signer test@example.com"))
}
