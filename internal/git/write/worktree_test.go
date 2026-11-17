package write

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// git is the oracle: worktree list says what each write did.
func TestWorktrees(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	h.r.Git("branch", "side")
	// A remote git knows, for the branch to track.
	h.r.Git("remote", "add", "origin", "https://example.invalid/r.git")
	h.r.Git("update-ref", "refs/remotes/origin/feature", "HEAD")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	list := func() string { return h.r.Git("worktree", "list", "--porcelain") }
	// as is a path as git lists it: with forward slashes on Windows too.
	as := filepath.ToSlash

	side := filepath.Join(base, "side")
	op, err := AddWorktree(h.w, WorktreeOptions{Path: side, Branch: "side"})
	h.do(op, err, true)
	if !strings.Contains(list(), "worktree "+as(side)+"\n") || !strings.Contains(list(), "branch refs/heads/side") {
		t.Fatalf("list\n%s", list())
	}
	// From a remote branch, a new branch tracking it.
	feat := filepath.Join(base, "feature")
	op, err = AddWorktree(h.w, WorktreeOptions{Path: feat, NewBranch: "feature", Start: "origin/feature", Track: true})
	h.do(op, err, true)
	if up := strings.TrimSpace(h.r.Git("config", "branch.feature.merge")); up != "refs/heads/feature" {
		t.Fatalf("upstream %q", up)
	}
	// A branch from a commit, no upstream.
	fix := filepath.Join(base, "fix")
	op, err = AddWorktree(h.w, WorktreeOptions{Path: fix, NewBranch: "fix", Start: h.rev("HEAD")})
	h.do(op, err, true)
	if _, err := h.r.Run(nil, "config", "branch.fix.merge"); err == nil {
		t.Fatal("fix tracks something")
	}

	h.do(LockWorktree(h.w, fix, "on a stick"), nil, true)
	if !strings.Contains(list(), "locked on a stick") {
		t.Fatalf("list\n%s", list())
	}
	// Locked: git refuses to remove it.
	h.do(RemoveWorktree(h.w, fix, false), nil, false)
	h.do(UnlockWorktree(h.w, fix), nil, true)

	// Changes: refused unless forced.
	os.WriteFile(filepath.Join(fix, "a"), []byte("2"), 0o644)
	h.do(RemoveWorktree(h.w, fix, false), nil, false)
	h.do(RemoveWorktree(h.w, fix, true), nil, true)
	if _, err := os.Stat(fix); !os.IsNotExist(err) {
		t.Fatalf("folder left: %v", err)
	}

	// A folder gone: prune forgets it.
	os.RemoveAll(feat)
	if !strings.Contains(list(), "prunable") {
		t.Fatalf("list\n%s", list())
	}
	h.do(PruneWorktrees(h.w), nil, true)
	if strings.Contains(list(), as(feat)) || !strings.Contains(list(), as(side)) {
		t.Fatalf("list\n%s", list())
	}

	for _, o := range []WorktreeOptions{{Path: "rel", Branch: "side"}, {Path: base + "/x"}, {Path: base + "/y", NewBranch: "-x"}} {
		if _, err := AddWorktree(h.w, o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
}

// Removing a worktree, alone or with its branch, keeps its files first,
// ignored ones included, in a snapshot under the refs of the worktree it
// is removed from: its own go with it.
func TestRemovingAWorktreeKeepsItsFiles(t *testing.T) {
	h := newHarness(t)
	h.r.Write(".gitignore", ".env\n")
	h.r.Write("a", "1\n")
	h.r.Commit("one")
	h.r.Git("branch", "side")
	h.r.Git("branch", "other")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	add := func(branch string) string {
		dir := filepath.Join(base, branch)
		op, err := AddWorktree(h.w, WorktreeOptions{Path: dir, Branch: branch})
		h.do(op, err, true)
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET="+branch+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	kept := func(path, want string) {
		t.Helper()
		snap := strings.TrimSpace(h.r.Git("for-each-ref", "--sort=-refname", "--count=1", "--format=%(objectname)", "refs/worktree/ramus/snapshots/"))
		if snap == "" {
			t.Fatal("no snapshot kept")
		}
		if got, err := h.r.Run(nil, "show", snap+":"+path); err != nil || got != want {
			t.Fatalf("%s in the snapshot: %q %v", path, got, err)
		}
	}

	side := add("side")
	if err := os.WriteFile(filepath.Join(side, "a"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.do(RemoveWorktree(h.w, side, true), nil, true)
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatalf("folder left: %v", err)
	}
	kept(".env", "SECRET=side\n")
	kept("a", "changed\n")

	other := add("other")
	h.do(DeleteBranch(h.w, "refs/heads/other", h.rev("other"), true, other, h.seen()), nil, true)
	if _, err := os.Stat(other); !os.IsNotExist(err) || h.rev("other") != "" {
		t.Fatalf("worktree or branch left: %v", err)
	}
	kept(".env", "SECRET=other\n")
	h.undo()
	if h.rev("other") == "" {
		t.Fatal("undo did not bring the branch back")
	}
	if _, err := wait(t, h.q.Redo()); err != nil || h.rev("other") != "" {
		t.Fatalf("redo: %v", err)
	}
}
