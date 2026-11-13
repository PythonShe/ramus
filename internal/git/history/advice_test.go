package history

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestGraphAdvice(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("a")
	open := func() *commitgraph.Graph {
		g, err := commitgraph.Open(filepath.Join(r.GitDir(), "objects"))
		if errors.Is(err, commitgraph.ErrNotFound) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { g.Close() })
		return g
	}
	advice := func() (Advice, bool) {
		g := open()
		_, covered := Covered(g, refTips(t, r))
		return GraphAdvice(r.GitDir(), true, g, covered)
	}
	if _, ok := GraphAdvice(r.GitDir(), false, nil, false); ok {
		t.Error("advice where git would not use a graph")
	}
	a, ok := advice()
	if !ok || a.Cmd.Line() != "git --git-dir="+r.GitDir()+" commit-graph write --reachable --split --changed-paths" {
		t.Fatalf("no graph: %v %q", ok, a.Cmd.Line())
	}
	r.Git("commit-graph", "write", "--reachable")
	if a, ok = advice(); !ok || a.Reason == "" {
		t.Fatal("no advice for a graph without filters")
	}
	if _, err := testrun.Runner(t).Run(context.Background(), &a.Cmd); err != nil {
		t.Fatal(err)
	}
	if a, ok = advice(); ok {
		t.Fatalf("advice after writing: %s", a.Reason)
	}
	r.Commit("b")
	if _, ok = advice(); !ok {
		t.Error("no advice for a stale graph")
	}
}
