package history

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
	if !ok || a.Replace || a.Cmd.Line() != "git --git-dir="+r.GitDir()+" commit-graph write --reachable --split --changed-paths" {
		t.Fatalf("no graph: %v %q", ok, a.Cmd.Line())
	}
	r.Git("commit-graph", "write", "--reachable")
	if a, ok = advice(); !ok || !a.Replace || !strings.HasSuffix(a.Cmd.Line(), " --split=replace --changed-paths") {
		t.Fatalf("a graph without filters: %v %q", ok, a.Cmd.Line())
	}
	if _, err := testrun.Runner(t).Run(context.Background(), &a.Cmd); err != nil {
		t.Fatal(err)
	}
	if a, ok = advice(); ok {
		t.Fatalf("advice after writing: %q", a.Cmd.Line())
	}
	r.Commit("b")
	if a, ok = advice(); !ok || a.Replace {
		t.Errorf("a stale graph: %v %q", ok, a.Cmd.Line())
	}

	// A graph without generation numbers covers nothing, and its advice
	// replaces it rather than adding a layer on top.
	r.WriteGraphWithoutGenerations()
	g := open()
	if _, covered := Covered(g, refTips(t, r)); covered || g.HasGenerations() {
		t.Fatal("a graph without generation numbers covers history")
	}
	if a, ok = advice(); !ok || !a.Replace || !strings.HasSuffix(a.Cmd.Line(), " --split=replace --changed-paths") {
		t.Fatalf("no generations: %v %q", ok, a.Cmd.Line())
	}
	if _, err := testrun.Runner(t).Run(context.Background(), &a.Cmd); err != nil {
		t.Fatal(err)
	}
	if a, ok = advice(); ok {
		t.Fatalf("advice after replacing: %q", a.Cmd.Line())
	}
}
