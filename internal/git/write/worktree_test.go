package write

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/snapshot"
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
// ignored ones included but not ignored folders, in a snapshot under the
// refs of the worktree it is removed from: its own go with it.
func TestRemovingAWorktreeKeepsItsFiles(t *testing.T) {
	h := newHarness(t)
	h.r.Write(".gitignore", ".env\nnode_modules/\n")
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
		if err := os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "node_modules", "x", "index.js"), []byte("x"), 0o644); err != nil {
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
	snap := strings.TrimSpace(h.r.Git("for-each-ref", "--sort=-refname", "--count=1", "--format=%(objectname)", "refs/worktree/ramus/snapshots/"))
	if _, err := h.r.Run(nil, "cat-file", "-e", snap+":node_modules/x/index.js"); err == nil {
		t.Fatal("an ignored folder kept in the snapshot")
	}

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

// Pruning the worktrees whose folders were deleted keeps their
// snapshots, which only their git directories held, and leaves a locked
// one's where they are, as git leaves the worktree.
func TestPruneWorktreesKeepsTheirSnapshots(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	for _, b := range []string{"gone", "locked", "there"} {
		h.r.Git("branch", b)
		op, err := AddWorktree(h.w, WorktreeOptions{Path: filepath.Join(base, b), Branch: b})
		h.do(op, err, true)
		h.r.Git("-C", filepath.Join(base, b), "update-ref", snapshot.Prefix+b, "HEAD")
	}
	h.r.Git("worktree", "lock", filepath.Join(base, "locked"))
	for _, b := range []string{"gone", "locked"} {
		if err := os.RemoveAll(filepath.Join(base, b)); err != nil {
			t.Fatal(err)
		}
	}
	h.do(PruneWorktrees(h.w), nil, true)
	if kept := h.r.Git("for-each-ref", "--format=%(refname)", snapshot.Prefix); kept != snapshot.Prefix+"gone\n" {
		t.Fatalf("snapshots kept: %q", kept)
	}
	if l := h.r.Git("worktree", "list", "--porcelain"); strings.Contains(l, "/gone\n") || !strings.Contains(l, "locked") {
		t.Fatalf("worktrees:\n%s", l)
	}
}

// Pruning keeps the snapshots of a record that names the same folder as
// another, which git prunes as a duplicate though its folder is there: one
// worktree's folder deleted, another's moved into its place and repaired.
func TestPruneWorktreesKeepsADuplicateRecordsSnapshots(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	pa, pb := filepath.Join(base, "wa"), filepath.Join(base, "wb")
	for _, p := range []string{pa, pb} {
		b := filepath.Base(p)
		h.r.Git("branch", b)
		op, err := AddWorktree(h.w, WorktreeOptions{Path: p, Branch: b})
		h.do(op, err, true)
		h.r.Git("-C", p, "update-ref", snapshot.Prefix+b, "HEAD")
	}
	if err := os.RemoveAll(pa); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pb, pa); err != nil {
		t.Fatal(err)
	}
	h.r.Git("-C", pa, "worktree", "repair")
	named := func(id string) string {
		b, _ := os.ReadFile(filepath.Join(h.r.GitDir(), "worktrees", id, "gitdir"))
		return strings.TrimSpace(string(b))
	}
	if named("wa") != named("wb") {
		t.Skipf("the records name %q and %q after the repair", named("wa"), named("wb"))
	}
	h.do(PruneWorktrees(h.w), nil, true)
	held := h.r.Git("for-each-ref", "--format=%(refname)", snapshot.Prefix)
	es, _ := os.ReadDir(filepath.Join(h.r.GitDir(), "worktrees"))
	for _, e := range es {
		held += h.r.Git("--git-dir="+filepath.Join(h.r.GitDir(), "worktrees", e.Name()), "for-each-ref", "--format=%(refname)", snapshot.Prefix)
	}
	for _, b := range []string{"wa", "wb"} {
		if !strings.Contains(held, snapshot.Prefix+b+"\n") {
			t.Fatalf("%s's snapshot is under no ref: %q", b, held)
		}
	}
}

// A worktree whose folder was deleted outside the app is removed, and
// its branch deleted, as git does: there is nothing of it to keep first.
func TestWorktreeFolderGone(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	h.r.Git("branch", "gone")
	h.r.Git("branch", "gone-too")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	for _, b := range []string{"gone", "gone-too"} {
		op, err := AddWorktree(h.w, WorktreeOptions{Path: filepath.Join(base, b), Branch: b})
		h.do(op, err, true)
		// A snapshot taken there, which only its git directory holds.
		h.r.Git("-C", filepath.Join(base, b), "update-ref", snapshot.Prefix+b, "HEAD")
		if err := os.RemoveAll(filepath.Join(base, b)); err != nil {
			t.Fatal(err)
		}
	}
	h.do(RemoveWorktree(h.w, filepath.Join(base, "gone"), false), nil, true)
	h.do(DeleteBranch(h.w, "refs/heads/gone-too", h.rev("HEAD"), false, filepath.Join(base, "gone-too"), h.seen()), nil, true)
	if kept := h.r.Git("for-each-ref", "--format=%(refname)", snapshot.Prefix); kept != snapshot.Prefix+"gone\n"+snapshot.Prefix+"gone-too\n" {
		t.Fatalf("the worktrees' snapshots are not kept: %q", kept)
	}
	if h.rev("refs/heads/gone-too") != "" {
		t.Fatal("the branch is still there")
	}
	if l := h.r.Git("worktree", "list", "--porcelain"); strings.Contains(l, "gone") {
		t.Fatalf("still listed:\n%s", l)
	}
	// Undo brings the branch back.
	h.undo()
	if h.rev("refs/heads/gone-too") == "" {
		t.Fatal("undo did not bring the branch back")
	}
}
