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
	h.until("its signature", h.shows("Good signature by test@example.com, key SHA256:"))
	// One row says it, beside the kind of key that made it.
	if n := countText(h, "Signature"); n != 1 || !h.tt.HasText("SSH") {
		t.Fatalf("%d signature rows, texts %q", n, h.tt.Texts())
	}

	h.click("Branches and Tags")
	h.click("Tags")
	h.until("the tag", h.shows("signed"))
	h.menu("v1", "Check Signature")
	h.until("the tag's signature", h.shows("Good signature by test@example.com, key SHA256:"))

	// With no allowed signers git cannot check an SSH signature, and
	// says why; the commit is no less signed.
	r.Git("config", "--unset", "gpg.ssh.allowedSignersFile")
	r.Write("b.txt", "b")
	r.Git("add", "b.txt")
	r.Git("commit", "-q", "-S", "-m", "signed elsewhere")
	h.click("History")
	h.until("the new commit", h.shows("signed elsewhere"))
	h.click("signed elsewhere")
	h.until("why it is not checked", h.shows("Cannot be checked: gpg.ssh.allowedSignersFile"))
	if h.tt.HasText("Not signed") {
		t.Fatal("a signed commit shown as not signed")
	}
}

func countText(h *harness, s string) int {
	n := 0
	for _, x := range h.tt.Texts() {
		if x == s {
			n++
		}
	}
	return n
}
