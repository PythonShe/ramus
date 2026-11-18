package repo

import (
	"context"
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// A worktree is part of another repository when that one has a work
// tree of its own, which its config says, whatever its directory is
// named and wherever it is.
func TestAdded(t *testing.T) {
	added := func(dir string) bool {
		t.Helper()
		r, err := Open(context.Background(), testrun.Runner(t), dir, trustAll)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			r.Close()
			<-r.Life().Done()
		}()
		return r.Added()
	}

	g := gittest.Init(t)
	g.Write("a.txt", "1\n")
	g.Commit("first")
	plain := filepath.Join(t.TempDir(), "plain")
	g.Git("worktree", "add", "-q", plain)
	if added(g.Dir) || !added(plain) {
		t.Errorf("of a repository with a work tree: itself %v, its worktree %v", added(g.Dir), added(plain))
	}

	// A bare repository, by either name, has no work tree of its own.
	for _, name := range []string{"bare.git", ".git"} {
		top := t.TempDir()
		g.Git("clone", "-q", "--bare", g.Dir, filepath.Join(top, name))
		w := filepath.Join(top, "w")
		gittest.Open(t, filepath.Join(top, name)).Git("worktree", "add", "-q", w)
		if added(w) {
			t.Errorf("a worktree of the bare repository %s counts as added", name)
		}
	}

	// A git directory kept apart from its work tree is not bare.
	top := t.TempDir()
	apart := filepath.Join(top, "main")
	g.Git("clone", "-q", "--separate-git-dir", filepath.Join(top, "data"), g.Dir, apart)
	w := filepath.Join(top, "w")
	gittest.Open(t, apart).Git("worktree", "add", "-q", w)
	if added(apart) || !added(w) {
		t.Errorf("git directory apart: itself %v, its worktree %v", added(apart), added(w))
	}
}
