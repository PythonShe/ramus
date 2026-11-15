package session

import (
	"bytes"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/write"
)

// fullStatusRuns counts the full statuses among the lines logged.
func fullStatusRuns(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, " status --porcelain=v2") && !strings.Contains(l, " -- ") {
			n++
		}
	}
	return n
}

// Refs that move neither HEAD nor the stash, as a fetch moves them, are
// read without a status; HEAD moving reads one.
func TestRefsAloneKeepStatus(t *testing.T) {
	runner, logged := loggedRunner(t)
	up := gittest.Init(t)
	up.Write("u", "1")
	up.Commit("up")
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Git("remote", "add", "origin", up.Dir)
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Refs != nil })
	m.settle(t, s)
	before, read := fullStatusRuns(logged()), s.State.Refreshed

	r.Git("tag", "v1")
	r.Git("update-ref", "refs/remotes/elsewhere/main", "HEAD")
	m.until(t, "the new refs", func() bool { return s.State.Refs.Len() == 3 })
	op, err := write.Fetch(s.Where(), "origin", false)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Submit(op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Result(); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the fetched branch", func() bool { return refOID(s.State.Refs, "refs/remotes/origin/main") != nil })
	m.settle(t, s)
	if n := fullStatusRuns(logged()) - before; n != 0 || !s.State.Refreshed.Equal(read) {
		t.Fatalf("refs under refs/tags and refs/remotes ran status %d times", n)
	}

	r.Git("commit", "-q", "--allow-empty", "-m", "two")
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	m.until(t, "the status of the new commit", func() bool { return s.State.Status.Branch.OID == head })

	// The stash is the one ref besides HEAD's that status shows.
	rd := s.Repo.Refs()
	h, _ := rd.Head()
	l1, _ := rd.List()
	r.Git("tag", "v2")
	l2, _ := rd.List()
	if !keepsStatus(&h, &h, l1, l2) {
		t.Fatal("a new tag needs status")
	}
	r.Write("a", "2")
	r.Git("stash", "-q")
	l3, _ := rd.List()
	if keepsStatus(&h, &h, l2, l3) {
		t.Fatal("a new stash entry keeps status")
	}
	moved := h
	moved.OID = bytes.Repeat([]byte{1}, len(h.OID))
	if keepsStatus(&h, &moved, l1, l1) || keepsStatus(nil, &h, l1, l1) {
		t.Fatal("HEAD moving keeps status")
	}
}
