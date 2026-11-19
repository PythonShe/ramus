package session

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestToOldLines(t *testing.T) {
	// Old 1..10; new: line 3 replaced by two lines, line 6 deleted, a
	// line added after old 9.
	hunks := []diff.Hunk{
		{OldStart: 3, OldCount: 1, NewStart: 3, NewCount: 2},
		{OldStart: 6, OldCount: 1, NewStart: 6, NewCount: 0},
		{OldStart: 9, OldCount: 0, NewStart: 10, NewCount: 1},
	}
	for _, c := range []struct {
		from, to, lo, hi int
		ok               bool
	}{
		{1, 2, 1, 2, true},
		{3, 4, 3, 3, true},    // the replacement stands for old 3
		{5, 5, 4, 4, true},    // unchanged, one line down
		{6, 7, 5, 7, true},    // around the deletion of old 6
		{10, 10, 0, 0, false}, // only the work tree has it
		{9, 11, 9, 10, true},  // the added line between has none
		{2, 11, 2, 10, true},
	} {
		lo, hi, ok := toOldLines(hunks, c.from, c.to)
		if lo != c.lo || hi != c.hi || ok != c.ok {
			t.Errorf("%d..%d: got %d..%d %t, want %d..%d %t", c.from, c.to, lo, hi, ok, c.lo, c.hi, c.ok)
		}
	}
}

func TestLineLog(t *testing.T) {
	r := gittest.Init(t)
	// A name git could read as an option or a revision.
	name := "-g:h"
	if !gittest.Portable(name) {
		name = "-g h"
	}
	r.Write("f", "a\nb\nc\nd\n")
	r.Commit("one")
	r.Write("f", "a\nB\nc\nd\n")
	r.Commit("two")
	r.Git("mv", "--", "f", name)
	r.Write(name, "a\nB\nc\nD\n")
	r.Commit("three")
	r.Write(name, "new\na\nB\nc\nD\n")
	r.Write("untracked", "x\n")
	s, m := openFor(t, r)

	read := func(path string, from, to int) (*LineLog, error) {
		var l *LineLog
		var err error
		done := false
		s.LineLog(path, from, to, func(got *LineLog, e error) { l, err, done = got, e, true })
		m.until(t, "the line log", func() bool { return done })
		return l, err
	}
	// Line 3 of the work tree is line 2 of HEAD, which "two" changed, in
	// the file as it was named then.
	l, err := read(name, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, c := range l.Commits {
		subjects = append(subjects, c.Subject+" "+c.Path)
	}
	if l.From != 2 || l.To != 2 || strings.Join(subjects, ",") != "two f,one f" {
		t.Fatalf("lines %d..%d: %q", l.From, l.To, subjects)
	}
	if p := string(l.Patch(0)); !strings.HasPrefix(p, "diff --git") || !strings.Contains(p, "-b\n+B\n") {
		t.Fatalf("patch of two:\n%s", p)
	}
	if _, err := diff.Parse(l.Patch(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := read(name, 1, 1); !errors.Is(err, ErrNotCommitted) {
		t.Fatalf("a line only the work tree has: %v", err)
	}
	if _, err := read("untracked", 1, 1); !errors.Is(err, ErrNotCommitted) {
		t.Fatalf("an untracked file: %v", err)
	}
}

// A file named HEAD at the top of the work tree makes HEAD ambiguous to
// git where no "--" ends the revisions: line history and the worktree
// line counts end theirs.
func TestFileNamedHEAD(t *testing.T) {
	r := gittest.Init(t)
	r.Write("HEAD", "not a revision\n")
	r.Write("a", "one\n")
	r.Commit("one")
	r.Write("a", "one\ntwo\n")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the worktrees", func() bool { return len(s.State.Worktrees) == 1 })
	var l *LineLog
	var err error
	done := false
	s.LineLog("a", 1, 1, func(got *LineLog, e error) { l, err, done = got, e, true })
	m.until(t, "the line log", func() bool { return done })
	if err != nil || len(l.Commits) != 1 {
		t.Fatalf("line log: %v", err)
	}
	var sum *WorktreeSummary
	stop := s.SummarizeWorktrees(s.State.Worktrees, func(got WorktreeSummary) { sum = &got })
	defer stop()
	m.until(t, "the summary", func() bool { return sum != nil })
	if sum.Err != nil || sum.Added != 1 {
		t.Fatalf("summary %+v", sum)
	}
}

// The lines followed are the file's own, as the work tree has them: a
// textconv driver's text, which may not even have the same lines, is not
// what git log -L follows nor what it shows.
func TestLineLogFollowsTheFileNotItsTextconv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the driver is a shell command")
	}
	r := gittest.Init(t)
	r.Write(".gitattributes", "*.txt diff=conv\n")
	r.Write("f.txt", "a\nb\nc\n")
	r.Commit("one")
	r.Write("f.txt", "a\nB\nc\n")
	r.Commit("two")
	// Each line twice, marked: line 2 of its text is the first line's.
	r.Git("config", "diff.conv.textconv", "sed -e p -e s/^/CONV:/")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	if s.State.Restricted {
		t.Fatal("not trusted")
	}
	var l *LineLog
	var err error
	done := false
	s.LineLog("f.txt", 2, 2, func(got *LineLog, e error) { l, err, done = got, e, true })
	m.until(t, "the line log", func() bool { return done })
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Commits) != 2 || l.Commits[0].Subject != "two" {
		t.Fatalf("commits %+v", l.Commits)
	}
	if p := string(l.Patch(0)); strings.Contains(p, "CONV:") || !strings.Contains(p, "-b\n+B\n") {
		t.Fatalf("patch of two:\n%s", p)
	}
}
