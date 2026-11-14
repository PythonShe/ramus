package repo

import (
	"context"
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestWorktreesAndSubmodules(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	b := filepath.Join(t.TempDir(), "b")
	c := filepath.Join(t.TempDir(), "c")
	r.Git("worktree", "add", "-q", b, "-b", "topic")
	r.Git("worktree", "add", "-q", "--detach", c)
	r.Git("worktree", "lock", "--reason", "on a stick", c)
	sub := gittest.Init(t)
	sub.Commit("sub")
	r.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", "--name", "lib one", sub.Dir, "vendor/lib")
	r.Commit("add submodule")

	rp, err := Open(context.Background(), testrun.Runner(t), r.Dir, func(Location) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	wts, err := rp.Worktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 3 || !wts[0].Current || wts[0].Branch != "refs/heads/main" || wts[1].Branch != "refs/heads/topic" ||
		!wts[2].Detached || !wts[2].Locked || wts[2].LockReason != "on a stick" || wts[2].Head == "" {
		t.Fatalf("worktrees %+v", wts)
	}
	subs, err := rp.Submodules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Name != "lib one" || subs[0].Path != "vendor/lib" || subs[0].URL != sub.Dir || !subs[0].Present {
		t.Fatalf("submodules %+v", subs)
	}
	// Deinitialized, its folder is empty: declared, not present.
	r.Git("submodule", "deinit", "-q", "-f", "vendor/lib")
	if subs, err = rp.Submodules(context.Background()); err != nil || len(subs) != 1 || subs[0].Present {
		t.Fatalf("submodules %+v, %v", subs, err)
	}
}
