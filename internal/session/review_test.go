package session

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// A pull request the session itself fetches between the clean-up's
// reading the refs and its write keeps its refs, as one another session
// fetches does; the others still go.
func TestForgetPullsKeepsWhatTheSessionFetchedSince(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	for _, n := range []string{"1", "3"} {
		r.Git("update-ref", "refs/ramus/pulls/"+n+"/head", "HEAD")
	}
	s, m := openSettled(t, r)
	logged := len(s.Log)
	s.ForgetPulls(nil)
	// The session has settled: what comes now is the refs read, handed
	// over for the write.
	read := <-m
	// What FetchPull records of the pull request it fetches.
	s.usePulls(s.Repo.CommonDir, []int{1}, true)
	read()
	m.until(t, "the clean-up", func() bool { return len(s.Log) > logged && !s.Log[len(s.Log)-1].Running })
	refs := r.Git("for-each-ref", "--format=%(refname)", "refs/ramus/pulls/")
	if !strings.Contains(refs, "refs/ramus/pulls/1/head") {
		t.Fatal("the clean-up deleted the ref of a pull request the session fetched since")
	}
	if strings.Contains(refs, "refs/ramus/pulls/3/head") {
		t.Fatal("the clean-up kept the ref of a pull request no one keeps")
	}
}
