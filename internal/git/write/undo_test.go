package write

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/refs"
	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

type undoFixture struct {
	t *testing.T
	r *gittest.Repo
	q *Queue
}

func newUndo(t *testing.T) *undoFixture {
	r, q, _ := newQueue(t)
	q.Refs = refs.New(r.GitDir(), r.GitDir(), 20)
	return &undoFixture{t: t, r: r, q: q}
}

func (f *undoFixture) do(name string, kind UndoKind, snap *snapshot.Options, args ...string) {
	f.t.Helper()
	op := Op{Name: name, Undo: kind, Snapshot: snap, Cmd: run.Cmd{GitDir: f.r.GitDir(), WorkTree: f.r.Dir, Args: args}}
	if _, err := wait(f.t, f.q.Submit(op)); err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
}

func (f *undoFixture) undo(want string) {
	f.t.Helper()
	if u, _ := f.q.History.Next(); u != want {
		f.t.Fatalf("next undo %q, want %q", u, want)
	}
	if _, err := wait(f.t, f.q.Undo()); err != nil {
		f.t.Fatalf("undo %s: %v", want, err)
	}
}

func (f *undoFixture) redo(want string) {
	f.t.Helper()
	if _, r := f.q.History.Next(); r != want {
		f.t.Fatalf("next redo %q, want %q", r, want)
	}
	if _, err := wait(f.t, f.q.Redo()); err != nil {
		f.t.Fatalf("redo %s: %v", want, err)
	}
}

// state is what undo must give back: HEAD, every branch, and the files.
func (f *undoFixture) state() string {
	f.t.Helper()
	head, _ := f.r.Run(nil, "symbolic-ref", "-q", "HEAD")
	oid, _ := f.r.Run(nil, "rev-parse", "-q", "--verify", "HEAD")
	return head + oid + f.r.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags") +
		f.r.Git("status", "--porcelain=v1", "-uall") + f.r.Git("diff", "--cached") + f.r.Git("diff")
}

func (f *undoFixture) roundTrip(name string, kind UndoKind, snap *snapshot.Options, args ...string) {
	f.t.Helper()
	before := f.state()
	f.do(name, kind, snap, args...)
	after := f.state()
	if after == before {
		f.t.Fatalf("%s changed nothing", name)
	}
	f.undo(name)
	if got := f.state(); got != before {
		f.t.Fatalf("undo %s:\n%s\nwant\n%s", name, got, before)
	}
	f.redo(name)
	if got := f.state(); got != after {
		f.t.Fatalf("redo %s:\n%s\nwant\n%s", name, got, after)
	}
}

func TestUndoRefsOnly(t *testing.T) {
	f := newUndo(t)
	f.r.Write("a", "1")
	f.r.Git("add", "a")
	f.roundTrip("Commit", RefsOnly, nil, "commit", "-q", "-m", "first")
	f.r.Stage("a", "2")
	f.roundTrip("Commit", RefsOnly, nil, "commit", "-q", "-m", "second")
	f.roundTrip("Amend", RefsOnly, nil, "commit", "-q", "--amend", "-m", "second, amended")
	f.roundTrip("Create branch", RefsOnly, nil, "branch", "topic")
	f.roundTrip("Delete branch", RefsOnly, nil, "branch", "-D", "topic")
	f.roundTrip("Rename branch", RefsOnly, nil, "branch", "-m", "main", "trunk")
	f.roundTrip("Tag", RefsOnly, nil, "tag", "v1")
	f.r.Git("checkout", "-q", "--detach")
	f.r.Write("a", "3")
	f.r.Git("add", "a")
	f.roundTrip("Commit detached", RefsOnly, nil, "commit", "-q", "-m", "detached")
}

func TestUndoMovesHead(t *testing.T) {
	f := newUndo(t)
	f.r.Write("a", "1")
	base := f.r.Commit("base")
	f.r.Git("checkout", "-q", "-b", "side")
	f.r.Write("b", "side")
	f.r.Commit("side")
	f.r.Git("checkout", "-q", "main")
	f.r.Write("c", "main")
	f.r.Commit("main")

	f.roundTrip("Checkout side", MovesHead, nil, "checkout", "-q", "side")
	f.roundTrip("New branch", MovesHead, nil, "checkout", "-q", "-b", "fresh")
	f.r.Git("checkout", "-q", "main")
	f.roundTrip("Detach", MovesHead, nil, "checkout", "-q", "--detach", base)
	f.r.Git("checkout", "-q", "main")
	f.roundTrip("Cherry-pick", MovesHead, nil, "cherry-pick", "side")
	f.r.Git("reset", "-q", "--hard", "HEAD~")
	f.roundTrip("Merge side", MovesHead, nil, "merge", "-q", "--no-edit", "side")
	f.r.Git("reset", "-q", "--hard", "HEAD~")
	f.roundTrip("Rebase", MovesHead, nil, "rebase", "-q", "side")
	f.roundTrip("Revert", MovesHead, nil, "revert", "--no-edit", "HEAD")

	// A hard reset over uncommitted work: undo brings the work back too.
	f.r.Write("a", "uncommitted")
	f.r.Write("untracked", "u")
	f.roundTrip("Reset", MovesHead, &snapshot.Options{Reason: "Reset", Paths: []string{"a", "untracked"}},
		"reset", "-q", "--hard", "HEAD~")
	if b, _ := os.ReadFile(filepath.Join(f.r.Dir, "a")); string(b) != "1" {
		t.Fatalf("redo kept %q", b)
	}
}

func TestUndoDiscard(t *testing.T) {
	f := newUndo(t)
	f.r.Write("a", "1")
	f.r.Commit("base")
	f.r.Write("a", "work")
	f.roundTrip("Discard", FilesOnly, &snapshot.Options{Reason: "Discard", Paths: []string{"a"}}, "checkout", "--", "a")
	f.r.Write("new", "n")
	f.roundTrip("Clean", FilesOnly, &snapshot.Options{Reason: "Clean", Paths: []string{"new"}}, "clean", "-q", "-f", "--", "new")
}

func TestUndoRefusesWhenChangedElsewhere(t *testing.T) {
	f := newUndo(t)
	f.r.Commit("one")
	f.do("Commit", RefsOnly, nil, "commit", "-q", "--allow-empty", "-m", "two")
	f.r.Commit("from the terminal")
	_, err := wait(t, f.q.Undo())
	var se *StaleError
	if !errors.As(err, &se) {
		t.Fatalf("undo over a terminal commit: %v", err)
	}
	if !strings.Contains(f.r.Git("log", "-1", "--format=%s"), "from the terminal") {
		t.Fatal("undo moved the branch anyway")
	}
	if _, err := wait(t, f.q.Redo()); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("redo with nothing undone: %v", err)
	}
	if u, _ := f.q.History.Next(); u != "Commit" {
		t.Fatalf("refused undo dropped the entry: %q", u)
	}
	f.do("Commit", RefsOnly, nil, "commit", "-q", "--allow-empty", "-m", "three")
	f.undo("Commit")
	f.do("Commit", RefsOnly, nil, "commit", "-q", "--allow-empty", "-m", "four")
	if _, r := f.q.History.Next(); r != "" {
		t.Fatalf("a new write kept redo %q", r)
	}
}
