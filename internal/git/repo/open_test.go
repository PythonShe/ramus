package repo

import (
	"context"
	"testing"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// Closing a repository stops its processes and releases its mappings, and
// the same repository opens again.
func TestOpenClose(t *testing.T) {
	g := gittest.Init(t)
	head := g.Commit("one")
	g.Git("commit-graph", "write", "--reachable")
	runner := testrun.Runner(t)
	for round := 0; round < 3; round++ {
		r, err := Open(context.Background(), runner, g.Dir)
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
