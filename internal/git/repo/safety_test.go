package repo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

func trustAll(Location) bool { return true }

func TestOwnership(t *testing.T) {
	g := gittest.Init(t)
	git, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	// git's own test switch makes it see another owner.
	other := run.New(git, append(gittest.Env(), "GIT_TEST_ASSUME_DIFFERENT_OWNER=1"), 2)
	_, err = Open(context.Background(), other, g.Dir, trustAll)
	var e *OwnershipError
	if !errors.As(err, &e) || e.Path != g.Dir || e.Fix != "git config --global --add safe.directory "+filepath.ToSlash(g.Dir) {
		t.Fatalf("got %v", err)
	}
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

func TestEmbeddedBare(t *testing.T) {
	g := gittest.Init(t)
	g.Commit("one")
	inner := filepath.Join(g.Dir, "vendor", "x.git")
	g.Git("init", "-q", "--bare", inner)
	if _, err := Open(context.Background(), testrun.Runner(t), inner, trustAll); !errors.Is(err, ErrEmbeddedBare) {
		t.Fatalf("embedded bare opened: %v", err)
	}
	// A bare repository on its own opens.
	alone := filepath.Join(t.TempDir(), "alone.git")
	g.Git("init", "-q", "--bare", alone)
	r, err := Open(context.Background(), testrun.Runner(t), alone, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

func TestRestricted(t *testing.T) {
	g := gittest.Init(t)
	g.Commit("one")
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, func(Location) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Restricted || !r.Runner.IsHardened() {
		t.Fatal("not restricted")
	}
	_, err = r.Runner.Run(context.Background(), &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Args: []string{"commit", "--allow-empty", "-m", "x"}})
	if !errors.Is(err, run.ErrRestricted) {
		t.Fatalf("write in a restricted repository: %v", err)
	}
	res, err := r.Runner.Run(context.Background(), &run.Cmd{GitDir: r.GitDir, Args: []string{"log", "--format=%s"}, Read: true})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "one" {
		t.Fatalf("read: %q %v", res.Stdout, err)
	}
}
