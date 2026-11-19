package session

import (
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// The clean-up of one window's session deletes no ref of a pull request
// another session of the same repository, another window, reviews; once
// that one closes, it does.
func TestForgetPullsKeepsWhatAnotherSessionReviews(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	for _, n := range []string{"1", "2", "3"} {
		r.Git("update-ref", "refs/ramus/pulls/"+n+"/head", "HEAD")
	}
	has := func(n string) bool {
		return strings.Contains(r.Git("for-each-ref", "--format=%(refname)", "refs/ramus/pulls/"), "refs/ramus/pulls/"+n+"/head")
	}
	// Both windows' sessions run on the one main thread.
	m := make(mainThread, 256)
	open := func() *Session {
		s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
		t.Cleanup(func() { s.Close(); <-s.Done() })
		m.until(t, "the status", func() bool { return s.State.Status != nil && s.State.Refs != nil })
		return s
	}
	a, b := open(), open()
	waitGone := func(n string) {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for has(n) {
			select {
			case fn := <-m:
				fn()
			case <-time.After(20 * time.Millisecond):
			case <-deadline:
				t.Fatalf("pull request %s's ref was never deleted", n)
			}
		}
	}
	// What FetchPull records of the pull request b reviews.
	b.usePulls(b.Repo.CommonDir, []int{1}, true)
	a.ForgetPulls([]int{2})
	// 3 goes in the same write as 1 would.
	waitGone("3")
	if !has("1") || !has("2") {
		t.Fatal("the clean-up deleted the ref of a pull request another session reviews")
	}
	b.Close()
	a.ForgetPulls([]int{2})
	waitGone("1")
	if !has("2") {
		t.Fatal("the clean-up deleted the ref of a pull request it keeps")
	}
}
