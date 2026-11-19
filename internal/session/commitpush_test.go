package session

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// A commit and push from a branch with no upstream goes to the remote
// the caller names, not the one it would otherwise choose.
func TestCommitPushKeepsRemote(t *testing.T) {
	origin, other := gittest.Init(t), gittest.Init(t)
	r := gittest.Init(t)
	r.Write("a.txt", "a\n")
	r.Commit("one")
	r.Git("checkout", "-q", "-b", "topic")
	r.Git("remote", "add", "origin", origin.Dir)
	r.Git("remote", "add", "other", other.Dir)
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	r.Write("a.txt", "b\n")
	r.Git("add", "a.txt")
	ended := false
	var err error
	if e := s.Commit(write.CommitOptions{Message: "two"}, &write.PushOptions{Remote: "other"}, func(e error) { err = e }); e != nil {
		t.Fatal(e)
	}
	pushed := func(g *gittest.Repo) bool {
		_, e := g.Run(nil, "rev-parse", "--verify", "-q", "refs/heads/topic")
		return e == nil
	}
	m.until(t, "the push", func() bool {
		ended = err != nil || pushed(other) || pushed(origin)
		return ended
	})
	if err != nil || !pushed(other) || pushed(origin) {
		t.Fatalf("pushed to other %v, to origin %v: %v", pushed(other), pushed(origin), err)
	}
}

// A commit and push with HEAD detached pushes nothing once a switch
// queued meanwhile put HEAD on a branch, which git push would send.
func TestCommitPushDetachedRefusesOnABranch(t *testing.T) {
	origin := gittest.Init(t, "--bare")
	r := gittest.Init(t)
	r.Write("a.txt", "a\n")
	r.Commit("one")
	r.Git("remote", "add", "origin", origin.Dir)
	r.Git("push", "-q", "-u", "origin", "main")
	r.Write("b.txt", "b\n")
	r.Commit("not to push")
	r.Git("switch", "-q", "--detach", "HEAD~1")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil && s.State.Head.Detached })
	r.Write("a.txt", "detached\n")
	r.Git("add", "a.txt")
	var err error
	ended := false
	if e := s.Commit(write.CommitOptions{Message: "two"}, &write.PushOptions{}, func(e error) { err, ended = e, true }); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Submit(write.Op{Name: "Switch", Cmd: run.Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"switch", "-q", "main"}}}); e != nil {
		t.Fatal(e)
	}
	m.until(t, "the push queued", func() bool { return ended })
	if err != nil {
		t.Fatal(err)
	}
	m.until(t, "the push", func() bool { return s.Queue.Pending() == 0 })
	if got := strings.TrimSpace(origin.Git("log", "-1", "--format=%s", "main")); got != "one" {
		t.Fatalf("origin main is %q", got)
	}
}
