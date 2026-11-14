package write

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// git signs a commit as commit.gpgSign says, or as asked; git
// verify-commit is the oracle.
func TestCommitSign(t *testing.T) {
	r, q, _ := newQueue(t)
	gittest.SSHSigning(t, r)
	w := Where{GitDir: r.GitDir(), WorkTree: r.Dir}
	signed := func() bool {
		return strings.Contains(r.Git("cat-file", "commit", "HEAD"), "\ngpgsig ")
	}
	commit := func(msg string, mode SignMode) {
		t.Helper()
		r.Write("a", msg)
		r.Git("add", "a")
		if _, err := wait(t, q.Submit(mustOp(t)(Commit(w, CommitOptions{Message: msg, Sign: mode})))); err != nil {
			t.Fatalf("%s: %v", msg, err)
		}
	}
	commit("as configured, off", SignAsConfigured)
	if signed() {
		t.Fatal("signed with commit.gpgSign unset")
	}
	commit("asked", SignYes)
	if !signed() {
		t.Fatal("not signed when asked")
	}
	if out, err := r.Run(nil, "verify-commit", "HEAD"); err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	r.Git("config", "commit.gpgSign", "true")
	commit("as configured, on", SignAsConfigured)
	if !signed() {
		t.Fatal("not signed with commit.gpgSign")
	}
	commit("declined", SignNo)
	if signed() {
		t.Fatal("signed when declined")
	}
}
