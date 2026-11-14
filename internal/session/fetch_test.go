package session

import (
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// A repository fetches in the background while asked to; a fetch that
// fails stops it, said once; an untrusted repository never fetches.
func TestBackgroundFetch(t *testing.T) {
	origin := gittest.Init(t)
	origin.Write("a.txt", "a\n")
	origin.Commit("one")
	r := gittest.Init(t)
	r.Git("remote", "add", "origin", origin.Dir)
	r.Git("fetch", "-q", "origin")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	fetches := func(s *Session) int {
		n := 0
		for _, l := range s.Log {
			if strings.Contains(l.Name, "in the background") {
				n++
			}
		}
		return n
	}

	want := origin.Commit("two")
	var failed []error
	s.SetBackgroundFetch(50*time.Millisecond, func(err error) { failed = append(failed, err) })
	m.until(t, "the fetch", func() bool {
		got, _ := r.Run(nil, "rev-parse", "refs/remotes/origin/main")
		return strings.TrimSpace(got) == want
	})

	r.Git("remote", "set-url", "origin", origin.Dir+"-gone")
	m.until(t, "the failure", func() bool { return len(failed) > 0 })
	if s.FetchStopped() == nil {
		t.Fatal("not stopped")
	}
	n := fetches(s)
	m.pump(300 * time.Millisecond)
	if fetches(s) != n || len(failed) != 1 {
		t.Fatalf("fetched %d more times after failing, told %d times", fetches(s)-n, len(failed))
	}

	u, mu := openFor(t, r)
	if !u.Repo.Restricted {
		t.Fatal("opened trusted")
	}
	u.SetBackgroundFetch(50*time.Millisecond, nil)
	mu.pump(300 * time.Millisecond)
	if fetches(u) != 0 {
		t.Fatal("an untrusted repository fetched")
	}
}
