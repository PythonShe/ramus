package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// Closing a repository stops its processes and releases its mappings, and
// the same repository opens again.
func TestOpenClose(t *testing.T) {
	g := gittest.Init(t)
	head := g.Commit("one")
	g.Git("commit-graph", "write", "--reachable")
	runner := testrun.Runner(t)
	for round := 0; round < 3; round++ {
		r, err := Open(context.Background(), runner, g.Dir, trustAll)
		if err != nil {
			t.Fatal(err)
		}
		h, err := r.Refs().Head()
		if err != nil || h.Target != "refs/heads/main" {
			t.Fatalf("head %+v %v", h, err)
		}
		graph, err := r.CommitGraph()
		if err != nil || graph == nil || graph.Len() != 1 {
			t.Fatalf("graph %v %v", graph, err)
		}
		cf, err := r.CatFile()
		if err != nil {
			t.Fatal(err)
		}
		if again, _ := r.CatFile(); again != cf {
			t.Error("a second cat-file process")
		}
		var typ string
		if err := cf.Read([][]byte{h.OID}, func(_ int, o catfile.Object) error { typ = o.Type; return nil }); err != nil || typ != "commit" {
			t.Fatalf("read %s: %q %v", head, typ, err)
		}
		r.Life().Go(func(ctx context.Context) { <-ctx.Done() })
		r.Close()
		select {
		case <-r.Life().Done():
		case <-time.After(5 * time.Second):
			t.Fatal("close did not finish")
		}
		if n := runner.Running(); n != 0 {
			t.Fatalf("%d processes after close", n)
		}
		if cf.Alive() {
			t.Error("cat-file alive after close")
		}
		if _, err := r.CatFile(); err == nil {
			t.Error("cat-file started after close")
		}
	}
}

// A commit-graph git writes after the repository opened is picked up, and
// the one returned before stays readable.
func TestCommitGraphReopens(t *testing.T) {
	g := gittest.Init(t)
	g.Commit("one")
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if graph, err := r.CommitGraph(); graph != nil || err != nil {
		t.Fatalf("graph before one was written: %v %v", graph, err)
	}
	g.Git("commit-graph", "write", "--reachable")
	first, err := r.CommitGraph()
	if err != nil || first == nil || first.Len() != 1 {
		t.Fatalf("graph %v %v", first, err)
	}
	if again, _ := r.CommitGraph(); again != first {
		t.Error("an unchanged graph opened again")
	}
	g.Commit("two")
	g.Git("commit-graph", "write", "--reachable")
	second, err := r.CommitGraph()
	if err != nil || second == first || second.Len() != 2 {
		t.Fatalf("rewritten graph %v %v", second, err)
	}
	if first.Len() != 1 || first.Lookup(first.OID(0)) != 0 {
		t.Error("the earlier graph no longer reads")
	}
}

// A config that changed after open is read again; in a repository not
// trusted, a filter it newly names does not run.
func TestReloadConfig(t *testing.T) {
	g := gittest.Init(t)
	g.Write("a.txt", "one\n")
	g.Write(".gitattributes", "*.txt filter=evil\n")
	g.Commit("one")
	runner := testrun.Runner(t)
	ctx := context.Background()

	r, err := Open(ctx, runner, g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	g.Git("config", "diff.tool", "later")
	if _, ok := r.Config().Get("diff.tool"); ok {
		t.Fatal("the config was read again unasked")
	}
	if err := r.ReloadConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Config().Get("diff.tool"); v != "later" {
		t.Fatalf("diff.tool %q after reload", v)
	}

	u, err := Open(ctx, runner, g.Dir, func(Location) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	marker := filepath.Join(t.TempDir(), "ran")
	g.Git("config", "filter.evil.clean", "touch "+marker+"; cat")
	g.Write("a.txt", "two\n")
	diff := func(rn *run.Runner) {
		t.Helper()
		if _, err := rn.Run(ctx, &run.Cmd{GitDir: g.GitDir(), WorkTree: g.Dir, Read: true, Args: []string{"diff", "--", "a.txt"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.ReloadConfig(ctx); err != nil {
		t.Fatal(err)
	}
	diff(u.Runner)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a filter named after open ran in a repository not trusted")
	}
	diff(runner) // the test's own check that the filter runs at all
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the filter never runs; the test proves nothing")
	}
}
