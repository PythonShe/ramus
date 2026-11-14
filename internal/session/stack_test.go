package session

import (
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// Branches stacked on each other are found and pushed in one action; a
// rebased stack goes out again under a lease, which holds against a push
// from elsewhere.
func TestPushStack(t *testing.T) {
	remote := gittest.Init(t, "--bare")
	r := gittest.Init(t)
	r.Write("f", "0")
	r.Commit("base")
	r.Git("remote", "add", "origin", remote.Dir)
	r.Git("push", "-q", "-u", "origin", "main")
	r.Git("remote", "set-head", "origin", "main")
	for _, b := range []string{"a", "b", "c"} {
		r.Git("checkout", "-q", "-b", b)
		r.Write(b, b)
		r.Commit(b)
	}
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil })
	load := func() RefInfo {
		var info *RefInfo
		s.LoadRefInfo(func(ri RefInfo) { info = &ri })
		m.until(t, "the branches", func() bool { return info != nil })
		return *info
	}
	info := load()
	want := []string{"refs/heads/a", "refs/heads/b", "refs/heads/c"}
	if got := info.StackOf("refs/heads/a"); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("stacks %v", info.Stacks)
	}
	push := func() error {
		j, err := s.PushStack(want)
		if err != nil {
			t.Fatal(err)
		}
		m.until(t, "the push", func() bool {
			select {
			case <-j.Done():
				return true
			default:
				return false
			}
		})
		_, err = j.Result()
		return err
	}
	remoteTip := func(b string) string { return strings.TrimSpace(remote.Git("rev-parse", b)) }
	if err := push(); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"a", "b", "c"} {
		if remoteTip(b) != strings.TrimSpace(r.Git("rev-parse", b)) {
			t.Fatalf("%s not pushed", b)
		}
		if up := strings.TrimSpace(r.Git("rev-parse", "--abbrev-ref", b+"@{upstream}")); up != "origin/"+b {
			t.Fatalf("%s tracks %q", b, up)
		}
	}

	// Rewrite the bottom; the stack moves with it and goes out again.
	r.Git("checkout", "-q", "c")
	r.Git("commit", "-q", "--amend", "-m", "c again")
	r.Git("checkout", "-q", "b")
	r.Git("commit", "-q", "--amend", "-m", "b again")
	r.Git("checkout", "-q", "c")
	r.Git("rebase", "-q", "--onto", "b", "c~1~1", "c")
	if err := push(); err != nil {
		t.Fatalf("a rewritten stack: %v", err)
	}
	if remoteTip("c") != strings.TrimSpace(r.Git("rev-parse", "c")) {
		t.Fatal("c not pushed again")
	}

	// Someone else moves c on the remote: the lease refuses.
	other := gittest.Init(t)
	other.Git("fetch", "-q", remote.Dir, "c")
	other.Git("checkout", "-q", "FETCH_HEAD")
	other.Write("x", "theirs")
	other.Commit("theirs")
	other.Git("push", "-q", remote.Dir, "HEAD:refs/heads/c")
	r.Write("y", "mine")
	r.Commit("mine")
	if err := push(); err == nil {
		t.Fatal("overwrote a push made elsewhere")
	}
}

// A rewrite of commits already pushed is told apart from one of commits
// not pushed yet, and from a branch behind a push made elsewhere.
func TestPushedRewrite(t *testing.T) {
	remote := gittest.Init(t, "--bare")
	r := gittest.Init(t)
	r.Write("f", "0")
	r.Commit("base")
	r.Git("remote", "add", "origin", remote.Dir)
	r.Git("push", "-q", "-u", "origin", "main")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil })
	head := func() string { return strings.TrimSpace(r.Git("rev-parse", "HEAD")) }
	check := func(old string) string {
		got, done := "", false
		s.PushedRewrite("refs/heads/main", old, func(up string) { got = up })
		// The check runs at most three commands; give it their time.
		go func() { time.Sleep(2 * time.Second); m.update(func() { done = true }) }()
		m.until(t, "the check", func() bool { return got != "" || done })
		return got
	}

	old := head()
	r.Git("commit", "-q", "--amend", "-m", "base, amended")
	if up := check(old); up != "origin/main" {
		t.Fatalf("an amended pushed commit: %q", up)
	}
	r.Git("reset", "-q", "--hard", old)

	r.Write("g", "1")
	r.Commit("local")
	old = head()
	r.Git("commit", "-q", "--amend", "-m", "local, amended")
	if up := check(old); up != "" {
		t.Fatalf("an amended commit never pushed: %q", up)
	}
}
