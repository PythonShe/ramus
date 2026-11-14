package write

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
)

func (h *harness) stash(n int) StashEntry {
	return StashEntry{N: n, Commit: h.rev("stash@{" + string(rune('0'+n)) + "}")}
}

func (h *harness) status() string { return strings.TrimSpace(h.r.Git("status", "--porcelain")) }

func TestStash(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Write("b", "1")
	h.r.Commit("one")
	h.r.Write("a", "staged")
	h.r.Git("add", "a")
	h.r.Write("b", "changed")
	h.r.Write("new", "untracked")

	// Keeping the index leaves the staged change in place; without
	// untracked files the new one stays too.
	h.do(Stash(h.w, StashOptions{Message: "first", KeepIndex: true}), nil, true)
	if got := h.status(); got != "M  a\n?? new" {
		t.Fatalf("status %q", got)
	}
	h.r.Git("reset", "-q", "--hard")
	h.do(Stash(h.w, StashOptions{Message: "second", Untracked: true}), nil, true)
	if got := h.status(); got != "" {
		t.Fatalf("status %q", got)
	}
	if list := h.r.Git("stash", "list"); !strings.Contains(list, "second") || !strings.Contains(list, "first") {
		t.Fatalf("list %q", list)
	}

	second, first := h.stash(0), h.stash(1)
	op, err := StashApply(h.w, second, false)
	h.do(op, err, true)
	if got := h.status(); got != "?? new" {
		t.Fatalf("after apply %q", got)
	}
	os.Remove(filepath.Join(h.r.Dir, "new"))
	// A stash seen at another place is refused.
	op, err = StashDrop(h.w, StashEntry{N: 0, Commit: first.Commit})
	h.do(op, err, false)
	op, err = StashDrop(h.w, second)
	h.do(op, err, true)
	if h.stash(0).Commit != first.Commit {
		t.Fatal("not dropped")
	}
	op, err = StashRestore(h.w, second.Commit, "second again")
	h.do(op, err, true)
	if h.stash(0).Commit != second.Commit {
		t.Fatal("not restored")
	}
	op, err = StashPop(h.w, h.stash(1), true)
	h.do(op, err, true)
	if got := h.status(); got != "M  a\n M b" {
		t.Fatalf("after pop %q", got)
	}
	if strings.Contains(h.r.Git("stash", "list"), "first") {
		t.Fatal("pop kept the stash")
	}
	h.r.Git("reset", "-q", "--hard")
	op, err = StashBranch(h.w, h.stash(0), "from-stash", h.seen())
	h.do(op, err, true)
	if h.seen().Branch != "refs/heads/from-stash" || h.r.Git("stash", "list") != "" {
		t.Fatalf("on %s, stash %q", h.seen().Branch, h.r.Git("stash", "list"))
	}
	if _, err := StashBranch(h.w, StashEntry{N: 0, Commit: second.Commit}, "-x", h.seen()); err == nil {
		t.Fatal("an option taken as a branch name")
	}
}

func TestCherryPickRevertAndClean(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	h.r.Git("switch", "-q", "-c", "topic")
	h.r.Write("b", "1")
	c1 := h.r.Commit("b")
	h.r.Write("c", "1")
	c2 := h.r.Commit("c")
	h.r.Git("switch", "-q", "main")
	main := h.rev("main")

	op, err := CherryPick(h.w, []string{c1, c2}, h.seen())
	h.do(op, err, true)
	if subj := h.r.Git("log", "-2", "--format=%s"); subj != "c\nb\n" {
		t.Fatalf("log %q", subj)
	}
	h.undo()
	if h.rev("main") != main {
		t.Fatal("cherry-pick not undone")
	}
	if _, err := CherryPick(h.w, []string{"--abort"}, h.seen()); err == nil {
		t.Fatal("an option taken as a commit")
	}
	h.r.Write("b", "2")
	cb := h.r.Commit("b again")
	op, err = Revert(h.w, []string{cb}, h.seen())
	h.do(op, err, true)
	if _, err := os.Stat(filepath.Join(h.r.Dir, "b")); !os.IsNotExist(err) {
		t.Fatal("not reverted")
	}

	h.r.Write(".gitignore", "*.log\n")
	h.r.Commit("ignore")
	h.r.Write("junk/x", "1")
	h.r.Write("y.log", "1")
	seen, err := Observe(h.w.WorkTree, []string{"junk/", "y.log"})
	if err != nil {
		t.Fatal(err)
	}
	h.r.Write("junk/late", "1")
	op, err = Clean(h.w, []string{"junk/", "y.log"}, true, seen)
	h.do(op, err, false)
	seen, _ = Observe(h.w.WorkTree, []string{"junk/", "y.log"})
	op, err = Clean(h.w, []string{"junk/", "y.log"}, true, seen)
	h.do(op, err, true)
	if got := h.status(); got != "" {
		t.Fatalf("left %q", got)
	}
	if _, err := os.Stat(filepath.Join(h.r.Dir, "y.log")); !os.IsNotExist(err) {
		t.Fatal("ignored file left")
	}
	h.undo()
	if b, err := os.ReadFile(filepath.Join(h.r.Dir, "y.log")); err != nil || string(b) != "1" {
		t.Fatalf("not restored: %v", err)
	}
}

func TestBisectAndPatches(t *testing.T) {
	h := newHarness(t)
	var commits []string
	for i := range 8 {
		h.r.Write("n", strings.Repeat("x", i+1))
		commits = append(commits, h.r.Commit("step"))
	}
	op, err := BisectStart(h.w, commits[7], []string{commits[0]}, h.seen())
	h.do(op, err, true)
	if _, err := os.Stat(filepath.Join(h.r.GitDir(), "BISECT_START")); err != nil {
		t.Fatal("not bisecting")
	}
	op, err = BisectMark(h.w, BisectBad, "", false)
	h.do(op, err, true)
	op, err = BisectMark(h.w, BisectGood, commits[1], false)
	h.do(op, err, true)
	h.do(BisectReset(h.w), nil, true)
	// The first mark can start it.
	op, err = BisectMark(h.w, BisectBad, commits[7], true)
	h.do(op, err, true)
	if !strings.Contains(h.r.Git("bisect", "log"), "# bad: [") {
		t.Fatal("not started by the mark")
	}
	h.do(BisectReset(h.w), nil, true)
	if h.rev("HEAD") != commits[7] {
		t.Fatal("not back where it started")
	}

	// A mailbox patch becomes commits; a plain diff changes files and is
	// undone from its snapshot.
	dir := t.TempDir()
	mbox := filepath.Join(dir, "c.patch")
	os.WriteFile(mbox, []byte(h.r.Git("format-patch", "-1", "--stdout", commits[7])), 0o644)
	h.r.Git("reset", "-q", "--hard", commits[6])
	op, err = ApplyPatch(h.w, mbox, true, nil, h.seen())
	h.do(op, err, true)
	if h.r.Git("log", "-1", "--format=%s") != "step\n" || h.rev("HEAD") == commits[6] {
		t.Fatal("not applied as a commit")
	}
	plain := filepath.Join(dir, "d.diff")
	h.r.Write("n", "changed")
	os.WriteFile(plain, []byte(h.r.Git("diff")), 0o644)
	h.r.Git("checkout", "--", "n")
	before := h.read("n")
	op, err = ApplyPatch(h.w, plain, false, []string{"n"}, h.seen())
	h.do(op, err, true)
	if h.read("n") != "changed" {
		t.Fatal("diff not applied")
	}
	h.undo()
	if h.read("n") != before {
		t.Fatal("apply not undone")
	}
}

func (h *harness) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(h.r.Dir, name))
	return string(b)
}

func TestStashPaths(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Write("b*", "1")
	h.r.Commit("one")
	h.r.Write("a", "2")
	h.r.Write("b*", "2")
	h.do(Stash(h.w, StashOptions{Paths: []string{"b*"}}), nil, true)
	if got := h.status(); got != "M a" {
		t.Fatalf("status %q", got)
	}
}

// Hunks picked by staging them are stashed, and the rest of the file
// stays as it was.
func TestStashStaged(t *testing.T) {
	h := newHarness(t)
	if g, err := gitexe.Find(context.Background(), os.Getenv("PATH")); err != nil || !g.Version.Has(gitexe.StashStaged) {
		t.Skip("no git with stash push --staged")
	}
	h.r.Write("f", "1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	h.r.Write("g", "g\n")
	h.r.Commit("one")
	h.r.Write("f", "1 picked\n2\n3\n4\n5\n6\n7\n8\n9 kept\n")
	h.r.Write("g", "g changed\n")
	// Stage the first hunk of f alone.
	patch := h.r.Git("diff", "-U0", "--", "f")
	first := patch[:strings.Index(patch, "@@ -9")]
	if out, err := h.r.Run([]byte(first), "apply", "--cached", "--unidiff-zero", "-"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	h.do(Stash(h.w, StashOptions{Staged: true, Message: "picked"}), nil, true)
	if got := h.r.Git("diff", "stash@{0}^1", "stash@{0}", "--", "f"); !strings.Contains(got, "+1 picked") || strings.Contains(got, "9 kept") {
		t.Fatalf("stash holds\n%s", got)
	}
	data, _ := os.ReadFile(filepath.Join(h.r.Dir, "f"))
	if string(data) != "1\n2\n3\n4\n5\n6\n7\n8\n9 kept\n" {
		t.Fatalf("f is %q", data)
	}
	if got := h.status(); got != "M f\n M g" {
		t.Fatalf("status %q", got)
	}
}
