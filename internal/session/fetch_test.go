package session

import (
	"os"
	"path/filepath"
	"runtime"
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

// Trusting a repository starts the background fetch, which never ran
// while it was restricted; a fetch the reopen cut off neither stays
// running nor reports the closing as a failure.
func TestTrustStartsTheBackgroundFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hanging ssh is a shell script")
	}
	origin := gittest.Init(t)
	origin.Write("a.txt", "a\n")
	want := origin.Commit("one")
	r := gittest.Init(t)
	r.Git("remote", "add", "origin", origin.Dir)
	s, m := openFor(t, r)
	var failed []error
	// As the app does, every frame.
	keep := func() {
		s.SetBackgroundFetch(50*time.Millisecond, func(err error) { failed = append(failed, err) })
	}
	fetched := func() bool {
		got, _ := r.Run(nil, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main")
		return strings.TrimSpace(got) == want
	}
	keep()
	m.pump(200 * time.Millisecond)
	if fetched() {
		t.Fatal("fetched while restricted")
	}

	if err := s.Trust(); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the fetch once trusted", func() bool { keep(); return fetched() })

	// A fetch that hangs until the repository closes.
	hang := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git("config", "core.sshCommand", hang)
	r.Git("remote", "set-url", "origin", "ssh://example.invalid/x")
	s.SetBackgroundFetch(0, nil)
	m.until(t, "the last background fetch", func() bool { return !s.Fetching() })
	pickerDone := false
	if !s.FetchFresh(0, func(error) { pickerDone = true }) {
		t.Fatal("no fetch started")
	}
	m.pump(100 * time.Millisecond)
	if err := s.Trust(); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the reopen", func() bool { return s.State.Status != nil })
	m.pump(300 * time.Millisecond)
	if s.Fetching() || pickerDone || len(failed) != 0 {
		t.Fatalf("after the reopen: fetching %v, the old fetch's done called %v, failures %v", s.Fetching(), pickerDone, failed)
	}
	r.Git("remote", "set-url", "origin", origin.Dir)
	var err error
	ended := false
	if !s.FetchFresh(0, func(e error) { err, ended = e, true }) {
		t.Fatal("a fetch cut off by the reopen still counts as running")
	}
	m.until(t, "the fetch after the reopen", func() bool { return ended })
	if err != nil {
		t.Fatal(err)
	}
}
