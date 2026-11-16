package session

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/forge"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// In a shallow clone, a pull request branched off below the history it
// holds has no merge base until fetched deeper, which LoadPullRange says
// apart from histories that share none.
func TestPullRangeShallow(t *testing.T) {
	host := gittest.Init(t)
	host.Write("a.txt", "0\n")
	base := host.Commit("base")
	host.Git("checkout", "-q", "-b", "thing")
	host.Write("b.txt", "b\n")
	head := host.Commit("head")
	host.Git("update-ref", "refs/pull/7/head", head)
	host.Git("checkout", "-q", "main")
	for _, n := range []string{"1", "2", "3", "4"} {
		host.Write("a.txt", n+"\n")
		host.Commit("main " + n)
	}

	dir := filepath.Join(t.TempDir(), "clone")
	gittest.Init(t).Git("clone", "-q", "--depth=1", "file://"+host.Dir, dir)
	r := gittest.Open(t, dir)
	r.Git("remote", "set-url", "origin", "https://github.com/o/r")
	r.Git("config", "url.file://"+host.Dir+".insteadOf", "https://github.com/o/r")

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	repo := forge.Repo{Kind: forge.GitHub, Host: "github.com", Path: "o/r"}

	rng := func(deepen int) (PullRange, error) {
		t.Helper()
		j, err := s.FetchPull(repo, 7, "main", deepen)
		if err != nil {
			t.Fatal(err)
		}
		m.until(t, "the fetch", func() bool {
			select {
			case <-j.Done():
				return true
			default:
				return false
			}
		})
		if _, err := j.Result(); err != nil {
			t.Fatalf("fetch: %v", err)
		}
		var got *PullRange
		var gotErr error
		s.LoadPullRange(7, func(pr PullRange, err error) { got, gotErr = &pr, err })
		m.until(t, "the range", func() bool { return got != nil })
		return *got, gotErr
	}
	_, err := rng(0)
	var none *NoMergeBaseError
	if !errors.As(err, &none) || !none.Shallow {
		t.Fatalf("shallow: %v", err)
	}
	pr, err := rng(200)
	if err != nil || strings.TrimSpace(r.Git("rev-parse", "--verify", base)) != hex.EncodeToString(pr.MergeBase) {
		t.Fatalf("deepened: %x, %v; want %s", pr.MergeBase, err, base)
	}
}
