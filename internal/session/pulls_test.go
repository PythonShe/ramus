package session

import (
	"os"
	"path/filepath"
	"runtime"
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
