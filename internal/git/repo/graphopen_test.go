package repo

import (
	"context"
	"sync"
	"testing"
	"time"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// slowGraphs makes CommitGraph's opens wait until released, telling each
// start on started, and records the graphs it closes.
type slowGraphs struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	closed  []*commitgraph.Graph
}

func newSlowGraphs(t *testing.T) *slowGraphs {
	sg := &slowGraphs{started: make(chan struct{}, 8), release: make(chan struct{})}
	open, close := openGraph, closeGraph
	t.Cleanup(func() { openGraph, closeGraph = open, close })
	openGraph = func(dir string, prev *commitgraph.Graph) (*commitgraph.Graph, error) {
		sg.started <- struct{}{}
		<-sg.release
		return open(dir, prev)
	}
	closeGraph = func(g *commitgraph.Graph) error {
		sg.mu.Lock()
		sg.closed = append(sg.closed, g)
		sg.mu.Unlock()
		return close(g)
	}
	return sg
}

func (sg *slowGraphs) closedGraphs() []*commitgraph.Graph {
	sg.mu.Lock()
	defer sg.mu.Unlock()
	return append([]*commitgraph.Graph(nil), sg.closed...)
}

func graphRepo(t *testing.T) *Repository {
	g := gittest.Init(t)
	g.Commit("one")
	g.Git("commit-graph", "write", "--reachable")
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); <-r.Life().Done() })
	return r
}

// The commit-graph is opened without the repository's lock: what else
// takes it goes on meanwhile. Two calls that open it at once map one
// graph between them, and the other is closed.
func TestCommitGraphOpensOutsideTheLock(t *testing.T) {
	r := graphRepo(t)
	sg := newSlowGraphs(t)
	type answer struct {
		g   *commitgraph.Graph
		err error
	}
	answers := make(chan answer, 2)
	open := func() {
		g, err := r.CommitGraph()
		answers <- answer{g, err}
	}
	go open()
	<-sg.started
	locked := make(chan struct{})
	go func() {
		r.CommitGraphBroken() // takes the lock
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(10 * time.Second):
		t.Fatal("the lock is held while the graph opens")
	}
	go open()
	<-sg.started
	close(sg.release)
	a, b := <-answers, <-answers
	if a.err != nil || b.err != nil || a.g == nil || a.g != b.g {
		t.Fatalf("answered %v %v and %v %v; want one graph", a.g, a.err, b.g, b.err)
	}
	if n := r.openGraphs(); n != 1 {
		t.Errorf("%d graphs mapped, want 1", n)
	}
	closed := sg.closedGraphs()
	if len(closed) != 1 || closed[0] == a.g {
		t.Errorf("closed %v of the two opened, the one kept being %v", closed, a.g)
	}
	if again, err := r.CommitGraph(); again != a.g || err != nil {
		t.Errorf("then answered %v %v", again, err)
	}
}

// A graph opened as the repository closes is closed, not left mapped
// with nothing to unmap it.
func TestCommitGraphOpenedAsTheRepositoryCloses(t *testing.T) {
	r := graphRepo(t)
	sg := newSlowGraphs(t)
	type answer struct {
		g   *commitgraph.Graph
		err error
	}
	answers := make(chan answer, 1)
	go func() {
		g, err := r.CommitGraph()
		answers <- answer{g, err}
	}()
	<-sg.started
	r.Close()
	close(sg.release)
	got := <-answers
	if got.g != nil || got.err == nil {
		t.Fatalf("answered %v %v from a repository closed", got.g, got.err)
	}
	if n := len(sg.closedGraphs()); n != 1 {
		t.Errorf("%d graphs closed, want the one opened", n)
	}
	<-r.Life().Done()
}
