package session

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ramus/internal/forge"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// markingGH is a gh that leaves a file behind when it runs, found through
// the environment it returns, and the file.
func markingGH(t *testing.T) ([]string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	script := "#!/bin/sh\ntouch '" + mark + "'\ncase \"$2\" in\nrepos/*) echo '[]' ;;\n*) echo '{\"data\":{\"repository\":{}}}' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// An empty config folder: gh is logged in to nothing but github.com.
	return []string{"PATH=" + dir + ":/usr/bin:/bin", "GH_CONFIG_DIR=" + t.TempDir()}, mark
}

// A repository not trusted asks no host about its pull requests, and one
// trusted asks no host gh is not logged in to, however its remote names
// it: the tool would run with the user's tokens.
func TestPullsAskOnlyKnownHosts(t *testing.T) {
	env, mark := markingGH(t)
	found := 0
	find := func(k forge.Kind) (*forge.CLI, error) {
		found++
		return forge.Find(env, k)
	}
	for _, tc := range []struct {
		name, remote string
		trust, asks  bool
	}{
		{"untrusted", "https://github.com/o/r.git", false, false},
		{"spoofed host", "https://github.evil.example/o/r.git", true, false},
		{"github.com", "https://github.com/o/r.git", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.Remove(mark)
			found = 0
			r := gittest.Init(t)
			r.Commit("one")
			r.Git("remote", "add", "origin", tc.remote)
			m := make(mainThread, 64)
			ts := trusted(t)
			if tc.trust {
				ts = trusted(t, r.GitDir())
			}
			s := Open(testrun.Runner(t), ts, r.Dir, m.update)
			t.Cleanup(func() { s.Close(); <-s.Done() })
			m.until(t, "the status", func() bool { return s.State.Status != nil })
			if s.State.Restricted == tc.trust {
				t.Fatalf("restricted %v, trusted %v", s.State.Restricted, tc.trust)
			}

			info := &RefInfo{
				Branches: []BranchInfo{{Name: "refs/heads/main", Upstream: "refs/remotes/origin/main"}},
				Remotes:  []RemoteInfo{{Name: "origin", URL: tc.remote}},
			}
			var res *PullsResult
			s.LoadPulls(find, info, func(got PullsResult) { res = &got })
			m.until(t, "the pull requests", func() bool { return res != nil })
			if res.Err != nil {
				t.Fatalf("pulls: %v", res.Err)
			}
			var cp *CommitPullResult
			oid := make([]byte, 20)
			s.LoadCommitPull(find, oid, "Subject", func(got CommitPullResult) { cp = &got })
			m.until(t, "the commit's pull request", func() bool { return cp != nil })
			if cp.Err != nil || cp.Found {
				t.Fatalf("commit pull: %+v", cp)
			}
			_, err := os.Stat(mark)
			if ran := err == nil; ran != tc.asks {
				t.Fatalf("gh ran %v, want %v", ran, tc.asks)
			}
			if !tc.trust && found != 0 {
				t.Fatalf("looked for gh %d times for an untrusted repository", found)
			}
		})
	}
}

// A commit of a fork's branch, whose pull request is open on the
// upstream, is named by GitHub only in the fork: it is asked there when
// the upstream names none, without the subject's number, which is one of
// the upstream's.
func TestCommitPullOfAFork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	sha := strings.Repeat("ab", 20)
	script := "#!/bin/sh\necho \"$2\" >> '" + filepath.Join(dir, "asked") + "'\ncase \"$2\" in\n" +
		"repos/me/r/commits/" + sha + "/pulls) echo '[{\"number\":7,\"title\":\"T\",\"html_url\":\"https://github.com/up/r/pull/7\",\"state\":\"open\"}]' ;;\n" +
		"repos/*/commits/*) echo '[]' ;;\n*) echo '{\"number\":0}' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + dir + ":/usr/bin:/bin", "GH_CONFIG_DIR=" + t.TempDir()}
	find := func(k forge.Kind) (*forge.CLI, error) { return forge.Find(env, k) }

	r := gittest.Init(t)
	r.Commit("one")
	r.Git("remote", "add", "origin", "https://github.com/me/r.git")
	r.Git("remote", "add", ParentRemote, "https://github.com/up/r.git")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })

	oid, _ := hex.DecodeString(sha)
	var cp *CommitPullResult
	s.LoadCommitPull(find, oid, "Subject (#3)", func(got CommitPullResult) { cp = &got })
	m.until(t, "the commit's pull request", func() bool { return cp != nil })
	if cp.Err != nil || !cp.Found || cp.Pull.Number != 7 || cp.Pull.URL != "https://github.com/up/r/pull/7" {
		t.Fatalf("commit pull: %+v", cp)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "asked"))
	want := "repos/up/r/commits/" + sha + "/pulls\nrepos/up/r/pulls/3\nrepos/me/r/commits/" + sha + "/pulls\n"
	if string(b) != want {
		t.Errorf("asked\n%s\nwant\n%s", b, want)
	}
}

// A fork's clone that names no upstream remote finds its branches' pull
// requests and its commits' merged ones in the repository GitHub says it
// was forked from, asked once.
func TestPullsOfAForkWithoutUpstream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	sha := strings.Repeat("ab", 20)
	script := "#!/bin/sh\necho \"$2 $8\" >> '" + filepath.Join(dir, "asked") + "'\ncase \"$2\" in\n" +
		"repos/me/r) echo '{\"parent\":{\"full_name\":\"up/r\"}}' ;;\n" +
		"repos/up/r/commits/" + sha + "/pulls) echo '[{\"number\":5,\"html_url\":\"https://github.com/up/r/pull/5\",\"state\":\"closed\",\"merged_at\":\"2026-11-01T00:00:00Z\"}]' ;;\n" +
		"graphql) case \"$8\" in owner=up) echo '{\"data\":{\"repository\":{\"b0\":{\"nodes\":[{\"number\":7,\"url\":\"https://github.com/up/r/pull/7\",\"state\":\"OPEN\",\"headRefName\":\"feature\",\"headRepositoryOwner\":{\"login\":\"me\"}}]}}}}' ;; *) echo '{\"data\":{\"repository\":{}}}' ;; esac ;;\n" +
		"*) echo '[]' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + dir + ":/usr/bin:/bin", "GH_CONFIG_DIR=" + t.TempDir()}
	find := func(k forge.Kind) (*forge.CLI, error) { return forge.Find(env, k) }

	r := gittest.Init(t)
	r.Commit("one")
	r.Git("remote", "add", "origin", "https://github.com/me/r.git")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })

	remotes := []RemoteInfo{{Name: "origin", URL: "https://github.com/me/r.git"}}
	info := &RefInfo{Branches: []BranchInfo{{Name: "refs/heads/feature", Upstream: "refs/remotes/origin/feature"}}, Remotes: remotes}
	var res *PullsResult
	s.LoadPulls(find, info, func(got PullsResult) { res = &got })
	m.until(t, "the pull requests", func() bool { return res != nil })
	if p := res.Pulls["refs/heads/feature"]; res.Err != nil || p.Number != 7 || p.Repo.Path != "up/r" {
		t.Fatalf("pulls: %+v", res)
	}
	if got := s.PullsRepo(forge.Repo{Kind: forge.GitHub, Host: "github.com", Path: "me/r"}, remotes); got.Path != "up/r" {
		t.Errorf("PullsRepo %+v, want the parent", got)
	}

	oid, _ := hex.DecodeString(sha)
	var cp *CommitPullResult
	s.LoadCommitPull(find, oid, "Subject", func(got CommitPullResult) { cp = &got })
	m.until(t, "the commit's pull request", func() bool { return cp != nil })
	if cp.Err != nil || !cp.Found || cp.Pull.Number != 5 || cp.Pull.State != forge.PullMerged {
		t.Fatalf("commit pull: %+v", cp)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "asked"))
	if n := strings.Count(string(b), "repos/me/r "); n != 1 {
		t.Errorf("asked for the parent %d times:\n%s", n, b)
	}
}
