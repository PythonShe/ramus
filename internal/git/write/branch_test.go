package write

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/refs"
)

func TestBranchSwitchAndReset(t *testing.T) {
	r, q, _ := newQueue(t)
	q.Refs = refs.New(r.GitDir(), r.GitDir(), 20)
	w := Where{GitDir: r.GitDir(), WorkTree: r.Dir}
	rev := func(x string) string { return strings.TrimSpace(r.Git("rev-parse", x)) }
	head := func() Seen {
		b := strings.TrimSpace(r.Git("rev-parse", "--symbolic-full-name", "HEAD"))
		if b == "HEAD" {
			b = ""
		}
		return Seen{Branch: b, Head: rev("HEAD")}
	}
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
	first := r.Commit("one")
	r.Write("a", "2")
	r.Commit("two")

	ok(CreateBranch(w, "topic", first, false, head()))
	if rev("topic") != first || !strings.HasSuffix(head().Branch, "/main") {
		t.Fatal("branch not made where asked, or HEAD moved")
	}
	var se *StaleError
	op, _ := CreateBranch(w, "topic", first, false, head())
	if _, err := wait(t, q.Submit(op)); !errors.As(err, &se) {
		t.Fatalf("second create: %v", err)
	}
	if _, err := CreateBranch(w, "-f", first, false, head()); err == nil {
		t.Fatal("a name starting with - was taken")
	}

	ok(Switch(w, "refs/heads/topic", head()), nil)
	if head().Branch != "refs/heads/topic" {
		t.Fatal("did not switch")
	}
	if _, err := wait(t, q.Undo()); err != nil || head().Branch != "refs/heads/main" {
		t.Fatalf("undo switch: %v, on %s", err, head().Branch)
	}

	ok(Detach(w, first, head()), nil)
	if head().Branch != "" || head().Head != first {
		t.Fatalf("detach: %+v", head())
	}
	ok(Switch(w, "main", head()), nil)

	// A hard reset keeps the dropped work in a snapshot and the branch
	// in a backup, and undoes.
	two := rev("HEAD")
	r.Write("a", "work in progress")
	ok(Reset(w, first, ResetHard, head()))
	if rev("main") != first {
		t.Fatal("reset did not move main")
	}
	if got, _ := os.ReadFile(filepath.Join(r.Dir, "a")); string(got) != "1" {
		t.Fatalf("a is %q", got)
	}
	if !strings.Contains(r.Git("for-each-ref", "--format=%(objectname)", BackupPrefix), two) {
		t.Fatal("no backup of main")
	}
	if _, err := wait(t, q.Undo()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(r.Dir, "a")); rev("main") != two || string(got) != "work in progress" {
		t.Fatalf("undo reset: main %s, a %q", rev("main"), got)
	}
	stale := head()
	stale.Head = first
	op, _ = Reset(w, first, ResetSoft, stale)
	if _, err := wait(t, q.Submit(op)); !errors.As(err, &se) {
		t.Fatalf("stale reset: %v", err)
	}
	if _, err := Reset(w, first, "sideways", head()); err == nil {
		t.Fatal("unknown mode taken")
	}
}
