package write

import (
	"errors"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/refs"
)

func TestCommitAmendAndPush(t *testing.T) {
	r, q, _ := newQueue(t)
	q.Refs = refs.New(r.GitDir(), r.GitDir(), 20)
	w := Where{GitDir: r.GitDir(), WorkTree: r.Dir}
	ok := func(op Op, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wait(t, q.Submit(op)); err != nil {
			t.Fatalf("%s: %v", op.Name, err)
		}
	}
	r.Write("a", "1")
	r.Git("add", "a")
	ok(Commit(w, CommitOptions{Message: "--not an option\n\nbody", SignOff: true, CoAuthors: []string{"Ada Lovelace <ada@example.com>"}}))
	msg := r.Git("log", "-1", "--format=%B")
	for _, want := range []string{"--not an option\n\nbody", "Signed-off-by: ", "Co-authored-by: Ada Lovelace <ada@example.com>"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message lacks %q:\n%s", want, msg)
		}
	}
	if _, err := Commit(w, CommitOptions{Message: "  "}); err == nil {
		t.Fatal("empty message")
	}
	if _, err := Commit(w, CommitOptions{Message: "x", CoAuthors: []string{"nobody"}}); err == nil {
		t.Fatal("bad co-author")
	}

	// A failing hook stops a commit, unless skipped.
	hook(t, r, "pre-commit", "echo no >&2; exit 1")
	r.Write("a", "2")
	r.Git("add", "a")
	if _, err := wait(t, q.Submit(mustOp(t)(Commit(w, CommitOptions{Message: "blocked"})))); err == nil {
		t.Fatal("hook did not stop the commit")
	}
	ok(Commit(w, CommitOptions{Message: "skipped hooks", NoVerify: true}))

	// Amend refuses when HEAD moved since it was seen, and backs up.
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	var se *StaleError
	op, _ := Commit(w, CommitOptions{Message: "amended", Amend: true, Seen: "0000000000000000000000000000000000000000", Branch: "refs/heads/main", NoVerify: true})
	if _, err := wait(t, q.Submit(op)); !errors.As(err, &se) {
		t.Fatalf("stale amend: %v", err)
	}
	ok(Commit(w, CommitOptions{Message: "amended", Amend: true, Seen: head, Branch: "refs/heads/main", NoVerify: true}))
	if s := strings.TrimSpace(r.Git("log", "-1", "--format=%s")); s != "amended" {
		t.Fatalf("subject %q", s)
	}
	if !strings.Contains(r.Git("for-each-ref", "--format=%(objectname)", BackupPrefix), head) {
		t.Fatal("no backup of the amended commit")
	}
	// Undo of the amend puts the branch back and keeps the index.
	if _, err := wait(t, q.Undo()); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != head {
		t.Fatalf("undo amend: HEAD %s, want %s", got, head)
	}

	// Push with an upstream set the first time, then plainly.
	remote := gittest.Init(t, "--bare")
	r.Git("remote", "add", "origin", remote.Dir)
	ok(Push(w, PushOptions{Remote: "origin"}), nil)
	if up := strings.TrimSpace(r.Git("rev-parse", "--abbrev-ref", "@{upstream}")); up != "origin/main" {
		t.Fatalf("upstream %q", up)
	}
	r.Write("a", "3")
	r.Git("add", "a")
	ok(Commit(w, CommitOptions{Message: "three", NoVerify: true}))
	ok(Push(w, PushOptions{}), nil)
	if got, want := strings.TrimSpace(remote.Git("rev-parse", "main")), strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != want {
		t.Fatalf("remote at %s, want %s", got, want)
	}
}

func mustOp(t *testing.T) func(Op, error) Op {
	return func(op Op, err error) Op {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
}
