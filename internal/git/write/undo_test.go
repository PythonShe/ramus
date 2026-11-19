package write

import (
	"errors"
	"fmt"
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
	f.doRefs(name, kind, snap, nil, args...)
}

func (f *undoFixture) doRefs(name string, kind UndoKind, snap *snapshot.Options, refs []string, args ...string) {
	f.t.Helper()
	op := Op{Name: name, Undo: kind, Snapshot: snap, Refs: refs, Cmd: run.Cmd{GitDir: f.r.GitDir(), WorkTree: f.r.Dir, Args: args}}
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
	f.roundTripRefs(name, kind, snap, nil, args...)
}

func (f *undoFixture) roundTripRefs(name string, kind UndoKind, snap *snapshot.Options, refs []string, args ...string) {
	f.t.Helper()
	before := f.state()
	f.doRefs(name, kind, snap, refs, args...)
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
	f.roundTripRefs("Create branch", RefsOnly, nil, []string{"refs/heads/topic"}, "branch", "topic")
	f.roundTripRefs("Delete branch", RefsOnly, nil, []string{"refs/heads/topic"}, "branch", "-D", "topic")
	f.roundTrip("Rename branch", RefsOnly, nil, "branch", "-m", "main", "trunk")
	f.roundTripRefs("Tag", RefsOnly, nil, []string{"refs/tags/v1"}, "tag", "v1")
	f.r.Git("checkout", "-q", "--detach")
	f.r.Write("a", "3")
	f.r.Git("add", "a")
	f.roundTrip("Commit detached", RefsOnly, nil, "commit", "-q", "-m", "detached")
}

// UndoEntry undoes only the write it names: 0, a write that recorded no
// entry, names none, and the last write stays done.
func TestUndoEntryOfNone(t *testing.T) {
	f := newUndo(t)
	f.r.Write("a", "1")
	f.r.Git("add", "a")
	f.do("Commit", RefsOnly, nil, "commit", "-q", "-m", "first")
	before := f.state()
	if _, err := wait(t, f.q.UndoEntry(0)); !errors.Is(err, ErrNotLast) {
		t.Fatalf("undo of no entry: %v", err)
	}
	if f.state() != before {
		t.Fatal("an earlier write was undone")
	}
	f.undo("Commit")
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

func TestUndoLeavesOtherRefsAlone(t *testing.T) {
	f := newUndo(t)
	f.r.Commit("one")
	// A hook stands in for the terminal creating a tag while the commit
	// runs.
	hook(t, f.r, "pre-commit", "git tag from-the-terminal")
	f.do("Commit", RefsOnly, nil, "commit", "-q", "--allow-empty", "-m", "two")
	f.undo("Commit")
	if out := f.r.Git("tag"); out != "from-the-terminal\n" {
		t.Fatalf("undo touched a tag it did not make: %q", out)
	}
}

func TestUndoDiscardOfADeletion(t *testing.T) {
	f := newUndo(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	os.Remove(filepath.Join(f.r.Dir, "a"))
	f.roundTrip("Discard", FilesOnly, &snapshot.Options{Reason: "Discard", Paths: []string{"a"}}, "checkout", "--", "a")
	if _, err := os.Stat(filepath.Join(f.r.Dir, "a")); err != nil {
		t.Fatal("redo did not bring the file back")
	}
}

func TestUndoDiscardOverStagedChange(t *testing.T) {
	f := newUndo(t)
	f.r.Write("a", "A")
	f.r.Commit("one")
	f.r.Stage("a", "B")
	f.r.Write("a", "A")
	f.roundTrip("Discard", FilesOnly, &snapshot.Options{Reason: "Discard", Paths: []string{"a"}}, "checkout", "--", "a")
}

func TestRedoKeepsWhatItDrops(t *testing.T) {
	f := newUndo(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	f.r.Write("a", "A")
	f.do("Discard", FilesOnly, &snapshot.Options{Reason: "Discard", Paths: []string{"a"}}, "checkout", "--", "a")
	f.undo("Discard")
	f.r.Write("a", "B")
	f.redo("Discard")
	f.undo("Discard")
	if b, _ := os.ReadFile(filepath.Join(f.r.Dir, "a")); string(b) != "B" {
		t.Fatalf("undo after redo restored %q, want the edit B", b)
	}
}

// Undo of a discard or a delete refuses to put files back over work done
// on them since, on disk or in the index, and puts them back once that
// work is gone again.
func TestUndoRefusesOverNewerWork(t *testing.T) {
	h := newHarness(t)
	h.r.Write("f", "base\n")
	h.r.Commit("base")
	h.r.Write("f", "first edit\n")
	h.do(Discard(h.w, []string{"f"}), nil, true)
	h.r.Write("f", "NEWER WORK\n")
	var stale *StaleError
	if _, err := wait(t, h.q.Undo()); !errors.As(err, &stale) {
		t.Fatalf("undo over newer work: %v", err)
	}
	if h.read("f") != "NEWER WORK\n" {
		t.Fatalf("f is %q", h.read("f"))
	}
	h.r.Git("checkout", "--", "f")
	h.undo()
	if h.read("f") != "first edit\n" {
		t.Fatalf("f is %q", h.read("f"))
	}

	h.r.Write("u", "untracked\n")
	op, err := Delete(h.w, []string{"u"})
	h.do(op, err, true)
	h.r.Write("u", "made again\n")
	if _, err := wait(t, h.q.Undo()); !errors.As(err, &stale) {
		t.Fatalf("undo of a delete over a file made since: %v", err)
	}
	if h.read("u") != "made again\n" {
		t.Fatalf("u is %q", h.read("u"))
	}

	h.r.Git("checkout", "--", "f")
	h.r.Write("f", "second edit\n")
	h.do(Discard(h.w, []string{"f"}), nil, true)
	h.r.Git("rm", "-q", "--cached", "f")
	if _, err := wait(t, h.q.Undo()); !errors.As(err, &stale) {
		t.Fatalf("undo over a change to the index: %v", err)
	}
}

// Undo of a branch delete or rename gives the branch its settings back,
// its upstream among them, and redo takes them along again.
func TestUndoKeepsBranchSettings(t *testing.T) {
	h := newHarness(t)
	h.r.Write("f", "1\n")
	h.r.Commit("base")
	h.r.Git("branch", "feat")
	h.r.Git("config", "branch.feat.remote", "origin")
	h.r.Git("config", "branch.feat.merge", "refs/heads/feat")
	settings := func(name string) string {
		out, _ := h.r.Run(nil, "config", "--get-regexp", `^branch\.`+name+`\.`)
		return strings.TrimSpace(out)
	}
	const feat = "branch.feat.remote origin\nbranch.feat.merge refs/heads/feat"
	h.do(DeleteBranch(h.w, "refs/heads/feat", h.rev("feat"), true, "", h.seen()), nil, true)
	if settings("feat") != "" {
		t.Fatalf("git kept %q", settings("feat"))
	}
	h.undo()
	if h.rev("feat") == "" || settings("feat") != feat {
		t.Fatalf("after undo: %q", settings("feat"))
	}
	if _, err := wait(t, h.q.Redo()); err != nil {
		t.Fatal(err)
	}
	if h.rev("feat") != "" || settings("feat") != "" {
		t.Fatalf("after redo: %q", settings("feat"))
	}
	h.undo()

	op, err := RenameBranch(h.w, "refs/heads/feat", "trunk", h.seen())
	h.do(op, err, true)
	if settings("trunk") == "" || settings("feat") != "" {
		t.Fatal("git did not move the settings")
	}
	h.undo()
	if settings("feat") != feat || settings("trunk") != "" {
		t.Fatalf("after undo: feat %q, trunk %q", settings("feat"), settings("trunk"))
	}
	if _, err := wait(t, h.q.Redo()); err != nil {
		t.Fatal(err)
	}
	if settings("feat") != "" || settings("trunk") == "" {
		t.Fatalf("after redo: feat %q, trunk %q", settings("feat"), settings("trunk"))
	}
}

func TestUndoHardResetKeepsStaged(t *testing.T) {
	f := newUndo(t)
	f.r.Write("a", "1")
	f.r.Commit("one")
	f.r.Commit("two")
	f.r.Stage("a", "staged")
	f.r.Write("a", "unstaged")
	f.roundTrip("Reset", MovesHead, &snapshot.Options{Reason: "Reset", Paths: []string{"a"}}, "reset", "-q", "--hard", "HEAD~")
}

// History holds no more than MaxHistoryBytes, as a patch each discard of
// lines keeps to redo it: the oldest entries go, the last stays though
// it alone is past the budget.
func TestHistoryHasAByteBudget(t *testing.T) {
	var h History
	patch := make([]byte, 20<<20)
	for i := range 6 {
		h.push(Entry{Name: fmt.Sprintf("Discard lines %d", i), op: Op{Input: patch}})
	}
	h.mu.Lock()
	n, total := len(h.done), 0
	for i := range h.done {
		total += h.done[i].size()
	}
	h.mu.Unlock()
	if total > MaxHistoryBytes || n == 0 {
		t.Fatalf("%d entries hold %d bytes", n, total)
	}
	if u, _ := h.Next(); u != "Discard lines 5" {
		t.Fatalf("next undo %q", u)
	}
	h.push(Entry{Name: "Huge", op: Op{Input: make([]byte, MaxHistoryBytes+1)}})
	if u, _ := h.Next(); u != "Huge" {
		t.Fatalf("next undo %q", u)
	}
}

// Redo of a delete git runs in several cleans, for its many paths,
// deletes them all again.
func TestRedoDeletesEveryPath(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	var paths []string
	for i := range 700 {
		p := fmt.Sprintf("untracked-file-with-a-rather-long-name-%04d.txt", i)
		h.r.Write(p, "x")
		paths = append(paths, p)
	}
	op, err := Delete(h.w, paths)
	if len(op.More) == 0 {
		t.Fatal("one clean: the test needs more paths")
	}
	h.do(op, err, true)
	h.undo()
	if _, err := wait(t, h.q.Redo()); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(h.r.Dir, p)); err == nil {
			t.Fatalf("%s left after redo", p)
		}
	}
}
