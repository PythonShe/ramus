//go:build !windows

package session

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/pathsafe"
	"ramus/internal/git/repo"
)

// pipeAt makes a named pipe at p. Should a read open it after all,
// unblock opens it for writing too, which lets that open return: a test
// of code that blocks fails rather than hangs. Register unblock after
// the session, so it runs before the session's cleanup waits for reads.
func pipeAt(t *testing.T, p string) (unblock func()) {
	t.Helper()
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Fatal(err)
	}
	return func() {
		if f, err := os.OpenFile(p, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	}
}

// A repository can register as a worktree a folder whose .git is a named
// pipe: its summary still comes, and the repository still closes.
func TestWorktreeWhoseGitIsAPipe(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	head := r.Commit("one")
	dir := filepath.Join(r.Dir, "vendor", "x")
	os.MkdirAll(dir, 0o755)
	adm := filepath.Join(r.GitDir(), "worktrees", "x")
	os.MkdirAll(adm, 0o755)
	os.WriteFile(filepath.Join(adm, "gitdir"), []byte(filepath.Join(dir, ".git")+"\n"), 0o644)
	os.WriteFile(filepath.Join(adm, "HEAD"), []byte(head+"\n"), 0o644)
	os.WriteFile(filepath.Join(adm, "commondir"), []byte("../..\n"), 0o644)
	unblock := pipeAt(t, filepath.Join(dir, ".git"))
	s, m := openFor(t, r)
	t.Cleanup(unblock)
	var wt *repo.Worktree
	m.until(t, "the worktree listed", func() bool {
		for i, w := range s.State.Worktrees {
			if filepath.Base(w.Path) == "x" {
				wt = &s.State.Worktrees[i]
			}
		}
		return wt != nil
	})
	if wt.Prunable {
		t.Fatalf("git took the worktree for one gone: %+v", *wt)
	}
	var sum *WorktreeSummary
	stop := s.SummarizeWorktrees([]repo.Worktree{*wt}, func(got WorktreeSummary) { sum = &got })
	defer stop()
	m.until(t, "the summary", func() bool { return sum != nil })
}

// A file of the work tree replaced by a named pipe is blamed as one that
// cannot be read, not waited on for ever.
func TestBlameOfAPipe(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	s, m := openFor(t, r)
	os.Remove(filepath.Join(r.Dir, "a.txt"))
	t.Cleanup(pipeAt(t, filepath.Join(r.Dir, "a.txt")))
	var err error
	done := false
	s.Blame("a.txt", func(_ []byte, e error) { err, done = e, true }, func(BlameUpdate) {})
	m.until(t, "the text", func() bool { return done })
	if err == nil {
		t.Fatal("a pipe was read as the file")
	}
}

// A conflicted file replaced by a named pipe is read as one that cannot
// be, not waited on for ever.
func TestConflictOfAPipe(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	r.Git("checkout", "-q", "-b", "other")
	r.Write("a.txt", "two\n")
	r.Commit("two")
	r.Git("checkout", "-q", "main")
	r.Write("a.txt", "three\n")
	r.Commit("three")
	r.Run(nil, "merge", "other")
	s, m := openFor(t, r)
	os.Remove(filepath.Join(r.Dir, "a.txt"))
	t.Cleanup(pipeAt(t, filepath.Join(r.Dir, "a.txt")))
	var cf *ConflictFile
	s.LoadConflict("a.txt", func(got ConflictFile) { cf = &got })
	m.until(t, "the conflict", func() bool { return cf != nil })
	if cf.Err == nil || !cf.Staged {
		t.Fatalf("a pipe was read as the file: %+v", *cf)
	}
}

// The terms of a bisect, read from the git directory, are not waited on
// in a named pipe.
func TestBisectTermsOfAPipe(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	os.WriteFile(filepath.Join(r.GitDir(), "BISECT_START"), []byte("main\n"), 0o644)
	unblock := pipeAt(t, filepath.Join(r.GitDir(), "BISECT_TERMS"))
	s, m := openFor(t, r)
	t.Cleanup(unblock)
	var st *BisectState
	s.LoadBisect(func(got BisectState, _ error) { st = &got })
	m.until(t, "the bisect", func() bool { return st != nil })
	if !st.Active || st.New != "bad" || st.Old != "good" {
		t.Fatalf("bisect %+v", *st)
	}
}

// An index that is a named pipe is not copied for a patch: the copy
// fails at once.
func TestIndexCopyOfAPipe(t *testing.T) {
	gitDir := t.TempDir()
	unblock := pipeAt(t, filepath.Join(gitDir, "index"))
	f, err := os.CreateTemp(t.TempDir(), "index")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := copyIndex(gitDir, f)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a pipe was copied as the index")
		}
	case <-time.After(time.Minute):
		unblock()
		t.Fatal("the copy waited on the pipe")
	}
}

// A patch file chosen to apply that is a named pipe is refused, not
// waited on for a writer that never comes.
func TestPatchFileThatIsAPipe(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	p := filepath.Join(t.TempDir(), "fix.patch")
	unblock := pipeAt(t, p)
	s, m := openFor(t, r)
	t.Cleanup(unblock)
	var got error
	done := false
	s.ReadPatchFile(p, func(_ PatchFile, err error) { got, done = err, true })
	m.until(t, "the patch file refused", func() bool { return done })
	if !errors.Is(got, pathsafe.ErrNotRegular) {
		t.Fatalf("read %v", got)
	}
}
