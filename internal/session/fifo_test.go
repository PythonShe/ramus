//go:build !windows

package session

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"ramus/internal/git/gittest"
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
