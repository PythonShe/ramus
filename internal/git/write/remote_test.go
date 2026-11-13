package write

import (
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/refs"
)

// harness is a repository with a queue, and helpers to run writes on it.
type harness struct {
	t *testing.T
	r *gittest.Repo
	q *Queue
	w Where
}

func newHarness(t *testing.T) *harness {
	r, q, _ := newQueue(t)
	q.Refs = refs.New(r.GitDir(), r.GitDir(), 20)
	return &harness{t: t, r: r, q: q, w: Where{GitDir: r.GitDir(), WorkTree: r.Dir}}
}

// rev is what x names, "" for nothing.
func (h *harness) rev(x string) string {
	out, _ := h.r.Run(nil, "rev-parse", "-q", "--verify", x)
	return strings.TrimSpace(out)
}

func (h *harness) seen() Seen {
	b := strings.TrimSpace(h.r.Git("rev-parse", "--symbolic-full-name", "HEAD"))
	if b == "HEAD" {
		b = ""
	}
	return Seen{Branch: b, Head: h.rev("HEAD")}
}

// do runs op, failing the test unless it succeeds as want says.
func (h *harness) do(op Op, err error, want bool) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
	_, err = wait(h.t, h.q.Submit(op))
	if (err == nil) != want {
		h.t.Fatalf("%s: %v", op.Name, err)
	}
}

func (h *harness) undo() {
	h.t.Helper()
	if _, err := wait(h.t, h.q.Undo()); err != nil {
		h.t.Fatal(err)
	}
}

func TestBranchRenameDeleteAndSwitchStashing(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	base := h.r.Commit("one")
	h.r.Git("branch", "topic")
	h.r.Git("switch", "-q", "topic")
	h.r.Write("b", "2")
	tip := h.r.Commit("on topic")
	h.r.Git("switch", "-q", "main")

	op, err := RenameBranch(h.w, "refs/heads/topic", "feature", h.seen())
	h.do(op, err, true)
	if h.rev("refs/heads/topic") != "" || h.rev("refs/heads/feature") != tip {
		t.Fatal("not renamed")
	}
	h.undo()
	if h.rev("refs/heads/topic") != tip || h.rev("refs/heads/feature") != "" {
		t.Fatal("rename not undone")
	}

	// Not merged: refused without force, kept with a backup with it.
	h.do(DeleteBranch(h.w, "refs/heads/topic", tip, false, "", h.seen()), nil, false)
	h.do(DeleteBranch(h.w, "refs/heads/topic", tip, true, "", h.seen()), nil, true)
	if h.rev("refs/heads/topic") != "" || !strings.Contains(h.r.Git("for-each-ref", BackupPrefix), tip) {
		t.Fatal("not deleted, or no backup")
	}
	h.undo()
	if h.rev("refs/heads/topic") != tip {
		t.Fatal("delete not undone")
	}
	// A stale view is refused.
	h.do(DeleteBranch(h.w, "refs/heads/topic", base, true, "", h.seen()), nil, false)

	// The worktree that has the branch goes with it.
	wt := filepath.Join(t.TempDir(), "wt")
	h.r.Git("worktree", "add", "-q", wt, "topic")
	h.do(DeleteBranch(h.w, "refs/heads/topic", tip, true, wt, h.seen()), nil, true)
	if h.rev("refs/heads/topic") != "" || strings.Contains(h.r.Git("worktree", "list"), "wt") {
		t.Fatal("branch or worktree left")
	}
	h.undo()

	// Switching with changes in the way: stashed first.
	h.r.Write("b", "local")
	h.r.Write("new", "untracked")
	h.do(SwitchWith(h.w, "refs/heads/topic", CarryChanges, h.seen()), nil, false)
	h.do(SwitchWith(h.w, "refs/heads/topic", StashChanges, h.seen()), nil, true)
	if h.seen().Branch != "refs/heads/topic" || !strings.Contains(h.r.Git("stash", "list"), "before switching to topic") ||
		strings.TrimSpace(h.r.Git("status", "--porcelain")) != "" {
		t.Fatalf("on %s, stash %q, status %q", h.seen().Branch, h.r.Git("stash", "list"), h.r.Git("status", "--porcelain"))
	}
}

func TestMergeAndRebase(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	h.r.Git("branch", "topic")
	h.r.Write("a", "2")
	h.r.Commit("main two")
	h.r.Git("switch", "-q", "topic")
	h.r.Write("b", "1")
	h.r.Commit("topic one")
	h.r.Git("switch", "-q", "main")
	main := h.rev("main")

	op, err := Merge(h.w, "topic", MergeFFOnly, h.seen())
	h.do(op, err, false)
	op, err = Merge(h.w, "topic", MergeSquash, h.seen())
	h.do(op, err, true)
	if h.rev("HEAD") != main || !strings.Contains(h.r.Git("diff", "--cached", "--name-only"), "b") {
		t.Fatal("squash moved HEAD or staged nothing")
	}
	h.r.Git("reset", "-q", "--hard")
	op, err = Merge(h.w, "topic", MergeNoFF, h.seen())
	h.do(op, err, true)
	if parents := strings.Fields(h.r.Git("log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("no merge commit: %v", parents)
	}
	h.undo()
	if h.rev("HEAD") != main {
		t.Fatal("merge not undone")
	}

	h.r.Git("switch", "-q", "topic")
	op, err = Rebase(h.w, "main", h.seen())
	h.do(op, err, true)
	if h.rev("topic~1") != main {
		t.Fatal("not rebased onto main")
	}
	if _, err := Rebase(h.w, "--root", h.seen()); err == nil {
		t.Fatal("an option taken as what to rebase onto")
	}
}

func TestRemotesFetchPullPushAndTags(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	bare := gittest.Init(t)
	bare.Git("config", "receive.denyCurrentBranch", "ignore")

	for _, bad := range []string{"", "-u evil", "ext::sh -c touch% /tmp/x", "https://example.com/\x1b[2Jrepo"} {
		if _, err := AddRemote(h.w, "x", bad); err == nil {
			t.Errorf("took %q", bad)
		}
	}
	op, err := AddRemote(h.w, "up", bare.Dir)
	h.do(op, err, true)
	op, err = RenameRemote(h.w, "up", "origin")
	h.do(op, err, true)
	op, err = SetRemoteURL(h.w, "origin", "file://"+bare.Dir)
	h.do(op, err, true)
	if got := strings.TrimSpace(h.r.Git("remote", "get-url", "origin")); got != "file://"+bare.Dir {
		t.Fatalf("url %q", got)
	}

	h.do(Push(h.w, PushOptions{Remote: "origin"}), nil, true)
	if bare.Git("rev-parse", "main") != h.r.Git("rev-parse", "main") {
		t.Fatal("not pushed")
	}
	// Another clone moves the remote branch: a pull fast-forwards to it,
	// and a force push of a rewrite is refused until it was fetched.
	other := gittest.Init(t)
	other.Git("fetch", "-q", bare.Dir, "main")
	other.Git("reset", "-q", "--hard", "FETCH_HEAD")
	other.Write("o", "1")
	other.Commit("other")
	other.Git("push", "-q", bare.Dir, "HEAD:main")
	op, err = Pull(h.w, PullFFOnly, h.seen())
	h.do(op, err, true)
	if h.rev("main") != strings.TrimSpace(bare.Git("rev-parse", "main")) {
		t.Fatal("not pulled")
	}
	h.r.Git("commit", "-q", "--amend", "-m", "rewritten")
	other.Write("o", "2")
	other.Commit("other again")
	other.Git("push", "-q", bare.Dir, "HEAD:main")
	h.do(Push(h.w, PushOptions{ForceWithLease: true}), nil, false)
	op, err = Fetch(h.w, "origin", true)
	h.do(op, err, true)
	h.r.Git("rebase", "-q", "origin/main")
	h.do(Push(h.w, PushOptions{ForceWithLease: true}), nil, true)

	// Prune drops a remote branch gone from the remote.
	bare.Git("branch", "gone", "main")
	op, err = Fetch(h.w, "", false)
	h.do(op, err, true)
	bare.Git("branch", "-D", "gone")
	op, err = Fetch(h.w, "", true)
	h.do(op, err, true)
	if h.rev("refs/remotes/origin/gone") != "" {
		t.Fatal("not pruned")
	}

	head := h.rev("HEAD")
	op, err = CreateTag(h.w, "v1", head, Annotated, "release one\n")
	h.do(op, err, true)
	op, err = CreateTag(h.w, "v1-light", head, Lightweight, "")
	h.do(op, err, true)
	if typ := strings.TrimSpace(h.r.Git("cat-file", "-t", "v1")); typ != "tag" || h.rev("v1-light") != head {
		t.Fatalf("v1 is a %s", typ)
	}
	if _, err := CreateTag(h.w, "v2", head, Annotated, " "); err == nil {
		t.Fatal("an annotated tag with no message")
	}
	if op, _ := CreateTag(h.w, "v3", head, Signed, "signed"); !strings.Contains(strings.Join(op.Cmd.Args, " "), "-s -F -") {
		t.Fatalf("signed tag args %q", op.Cmd.Args)
	}
	op, err = PushTag(h.w, "origin", "v1")
	h.do(op, err, true)
	if bare.Git("rev-parse", "v1") != h.r.Git("rev-parse", "v1") {
		t.Fatal("tag not pushed")
	}
	obj := h.rev("refs/tags/v1")
	h.do(DeleteTag(h.w, "v1", obj), nil, true)
	h.undo()
	if h.rev("refs/tags/v1") != obj {
		t.Fatal("tag delete not undone")
	}

	op, err = RemoveRemote(h.w, "origin")
	h.do(op, err, true)
	if strings.TrimSpace(h.r.Git("remote")) != "" {
		t.Fatal("remote left")
	}
}
