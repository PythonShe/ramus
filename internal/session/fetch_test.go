package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// A repository fetches in the background while asked to; a fetch that
// fails for a reason that may pass is tried again, waiting longer each
// time; one that needs the user stops it, said once; an untrusted
// repository never fetches.
func TestBackgroundFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing ssh is a shell script")
	}
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
	fetched := func(want string) func() bool {
		return func() bool {
			got, _ := r.Run(nil, "rev-parse", "refs/remotes/origin/main")
			return strings.TrimSpace(got) == want
		}
	}

	want := origin.Commit("two")
	var failed []error
	s.SetBackgroundFetch(50*time.Millisecond, func(err error) { failed = append(failed, err) })
	m.until(t, "the fetch", fetched(want))

	// A remote that is not there for now: tried again, later each time.
	r.Git("remote", "set-url", "origin", origin.Dir+"-gone")
	m.until(t, "two failures", func() bool { return s.fetch.failures >= 2 })
	if s.FetchStopped() != nil || len(failed) != 0 {
		t.Fatalf("a failure that may pass stopped fetching: %v, told %v", s.FetchStopped(), failed)
	}
	if w := s.fetch.wait(); w < 200*time.Millisecond {
		t.Fatalf("waits %v after %d failures", w, s.fetch.failures)
	}
	r.Git("remote", "set-url", "origin", origin.Dir)
	want = origin.Commit("three")
	m.until(t, "the fetch once the remote is back", fetched(want))
	m.until(t, "the failures forgotten", func() bool { return s.fetch.failures == 0 })

	// A host key ssh refuses needs the user.
	ssh := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\necho 'Host key verification failed.' >&2\nexit 255\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git("config", "core.sshCommand", ssh)
	r.Git("remote", "set-url", "origin", "ssh://example.invalid/x")
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

// The wait doubles with each failure, up to an hour, or the interval
// set when that is longer.
func TestFetchBackoff(t *testing.T) {
	for _, c := range []struct {
		every    time.Duration
		failures int
		want     time.Duration
	}{
		{15 * time.Minute, 0, 15 * time.Minute},
		{15 * time.Minute, 1, 30 * time.Minute},
		{15 * time.Minute, 2, time.Hour},
		{15 * time.Minute, 50, time.Hour},
		{2 * time.Hour, 3, 2 * time.Hour},
	} {
		bf := backgroundFetch{every: c.every, failures: c.failures}
		if got := bf.wait(); got != c.want {
			t.Errorf("every %v, %d failures: waits %v, want %v", c.every, c.failures, got, c.want)
		}
	}
}

// What git or ssh says when only the user can help, as opposed to a
// network that may come back.
func TestNeedsUser(t *testing.T) {
	for stderr, want := range map[string]bool{
		"fatal: could not read Username for 'https://example.com': terminal prompts disabled\n":                                        true,
		"remote: Invalid username or password.\nfatal: Authentication failed for 'https://example.com/x/'\n":                           true,
		"git@example.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n":                             true,
		"Host key verification failed.\nfatal: Could not read from remote repository.\n":                                               true,
		"remote: Repository not found.\nfatal: repository 'https://example.com/x/' not found\n":                                        true,
		"fatal: unable to access 'https://example.com/x/': The requested URL returned error: 403\n":                                    true,
		"ssh: Could not resolve hostname example.com: nodename nor servname provided\nfatal: Could not read from remote repository.\n": false,
		"fatal: unable to access 'https://example.com/x/': Could not resolve host: example.com\n":                                      false,
		"fatal: unable to access 'https://example.com/x/': The requested URL returned error: 502\n":                                    false,
	} {
		if got := needsUser(&run.Error{Line: "git fetch", ExitCode: 128, Stderr: stderr, Err: errors.New("exit status 128")}); got != want {
			t.Errorf("%q: needs the user %v", stderr, got)
		}
	}
	if needsUser(context.Canceled) {
		t.Error("a cancelled fetch needs the user")
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
