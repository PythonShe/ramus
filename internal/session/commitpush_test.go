package session

import (
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
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
