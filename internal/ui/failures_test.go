package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/config"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/write"
)

// A push the remote rejects for being behind says so, and Pull and Push
// takes the remote's commits in and pushes.
func TestPushBehindPullsAndPushes(t *testing.T) {
	h := newHarness(t)
	origin := gittest.Init(t, "--bare")
	other := h.repoWith()
	other.Git("remote", "add", "origin", origin.Dir)
	other.Git("push", "-q", "origin", "HEAD:refs/heads/main")
	r := gittest.Init(t)
	r.Git("remote", "add", "origin", origin.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("reset", "-q", "--hard", "origin/main")
	r.Git("branch", "-q", "--set-upstream-to=origin/main")
	other.Write("theirs", "x")
	theirs := other.Commit("theirs")
	other.Git("push", "-q", "origin", "HEAD:refs/heads/main")
	r.Write("mine", "x")
	r.Commit("mine")

	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	if _, err := tb.s.Submit(write.Push(tb.s.Where(), write.PushOptions{})); err != nil {
		t.Fatal(err)
	}
	h.until("the rejection", h.shows("Push failed: the remote branch has commits this branch does not have"))
	h.click("Pull and Push")
	pushed := func() bool {
		head, _ := r.Run(nil, "rev-parse", "HEAD")
		remote, _ := origin.Run(nil, "rev-parse", "main")
		_, notIn := r.Run(nil, "merge-base", "--is-ancestor", theirs, "HEAD")
		return notIn == nil && strings.TrimSpace(head) == strings.TrimSpace(remote)
	}
	h.until("pulled and pushed", pushed)
	h.until("the bar gone", func() bool { return !h.tt.HasText("Push failed") })
}

// A switch git refuses over local changes offers to stash them and try
// again.
func TestStashAndTryAgain(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("switch", "-q", "-c", "topic")
	r.Write("README", "topic")
	r.Commit("on topic")
	r.Git("switch", "-q", "main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("README", "mine")
	tb := h.app.current()
	h.until("the change", func() bool { return tb.s.State.Status != nil && tb.s.State.Status.Len() == 1 })
	if _, err := tb.s.Submit(write.Switch(tb.s.Where(), "topic", tb.s.Seen())); err != nil {
		t.Fatal(err)
	}
	h.until("the refusal", h.shows("Switch to topic failed: it would overwrite your uncommitted changes"))
	h.click("Stash and Try Again")
	h.until("switched", func() bool { return strings.TrimSpace(r.Git("branch", "--show-current")) == "topic" })
	if s := r.Git("stash", "list"); !strings.Contains(s, "before Switch to topic") {
		t.Fatalf("stashes %q", s)
	}
}

// A lock a crashed git left behind can be removed from the error bar,
// and the write runs again.
func TestRemoveStaleLock(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("a.txt"))
	tb := h.app.current()
	tb.s.Queue.LockWait = 50 * time.Millisecond
	lock := filepath.Join(r.GitDir(), "index.lock")
	staleLock(t, lock)
	if _, err := tb.s.Submit(write.Stage(tb.s.Where(), []string{"a.txt"})); err != nil {
		t.Fatal(err)
	}
	h.until("the lock", h.shows("a git command that ended left index.lock behind"))
	h.click("Remove Lock and Try Again")
	h.until("staged", func() bool { return strings.Contains(r.Git("diff", "--cached", "--name-only"), "a.txt") })
	if _, err := os.Stat(lock); err == nil {
		t.Fatal("the lock is still there")
	}
}

// A discard that fails part way, after it may have dropped work, says
// where that work is kept.
func TestFailedDiscardSaysWhereTheWorkIs(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write(".gitattributes", "a filter=broken\n")
	r.Write("a", "1\n")
	r.Commit("filter")
	r.Git("config", "filter.broken.clean", "cat")
	r.Git("config", "filter.broken.smudge", "false")
	r.Git("config", "filter.broken.required", "true")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("a", "work\n")
	tb := h.app.current()
	if _, err := tb.s.Submit(write.Discard(tb.s.Where(), []string{"a"})); err != nil {
		t.Fatal(err)
	}
	h.until("the failure", h.shows("is kept in a snapshot, and Undo puts it back."))
}

// What Notify says stays, each until dismissed, rather than the last
// taking the place of the ones before.
func TestWindowNotesStack(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.Notify("settings not read")
	h.app.Notify("library not read")
	h.until("both", func() bool { return h.tt.HasText("settings not read") && h.tt.HasText("library not read") })
}

func TestPullMode(t *testing.T) {
	for _, c := range []struct {
		set  [][]string
		want write.PullMode
	}{
		{nil, write.PullMerge},
		{[][]string{{"pull.rebase", "true"}}, write.PullRebase},
		{[][]string{{"pull.ff", "only"}}, write.PullFFOnly},
		{[][]string{{"pull.rebase", "true"}, {"branch.main.rebase", "false"}}, write.PullMerge},
		{[][]string{{"branch.main.rebase", "merges"}}, write.PullRebaseMerges},
	} {
		r := gittest.Init(t)
		for _, kv := range c.set {
			r.Git(append([]string{"config"}, kv...)...)
		}
		cfg, err := config.Read(context.Background(), testrun.Runner(t), r.GitDir())
		if err != nil {
			t.Fatal(err)
		}
		if got := pullMode(cfg, "refs/heads/main"); got != c.want {
			t.Fatalf("with %v: %s, want %s", c.set, got, c.want)
		}
	}
}

// A write that fails during a merge with conflicts for a reason of its
// own, a lock left behind, says so, rather than leaving it to the
// merge's bar.
func TestFailureDuringConflicts(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/lsof"); err != nil {
		if _, err := os.Stat("/usr/bin/lsof"); err != nil {
			t.Skip("no lsof")
		}
	}
	h := newHarness(t)
	r := h.repoWith("a.txt")
	r.Git("switch", "-q", "-c", "side")
	r.Write("a.txt", "side\n")
	r.Commit("side")
	r.Git("switch", "-q", "main")
	r.Write("a.txt", "main\n")
	r.Commit("main")
	if _, err := r.Run(nil, "merge", "side"); err == nil {
		t.Fatal("the merge did not conflict")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the conflict", func() bool { st := tb.s.State.Status; return st != nil && st.Unmerged() == 1 })
	tb.s.Queue.LockWait = 50 * time.Millisecond
	staleLock(t, filepath.Join(r.GitDir(), "index.lock"))
	if _, err := tb.s.Submit(write.Stage(tb.s.Where(), []string{"a.txt"})); err != nil {
		t.Fatal(err)
	}
	h.until("the lock", h.shows("a git command that ended left index.lock behind"))
}

// staleLock leaves a lock as a git that crashed long enough ago would.
func staleLock(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-write.LeftAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// A lock no process has open but made a moment ago may be a commit's,
// running a hook: it is not offered for removal.
func TestFreshLockInUse(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("a.txt"))
	tb := h.app.current()
	tb.s.Queue.LockWait = 50 * time.Millisecond
	if err := os.WriteFile(filepath.Join(r.GitDir(), "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.s.Submit(write.Stage(tb.s.Where(), []string{"a.txt"})); err != nil {
		t.Fatal(err)
	}
	h.until("in use", h.shows("index.lock is in use by a git command running now"))
	if h.tt.HasText("Remove Lock and Try Again") || !h.tt.HasText("Try Again") {
		t.Fatalf("fixes: %q", h.tt.Texts())
	}
}
