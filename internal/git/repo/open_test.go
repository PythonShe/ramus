package repo

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/history"
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
		case <-time.After(time.Minute):
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

// A cat-file process that died is waited for when another replaces it,
// not left for the repository's close.
func TestCatFileReplacesTheDead(t *testing.T) {
	if _, err := exec.LookPath("pkill"); err != nil {
		t.Skip(err)
	}
	g := gittest.Init(t)
	oid, _ := hex.DecodeString(g.Commit("one"))
	runner := testrun.Runner(t)
	r, err := Open(context.Background(), runner, g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for round := 0; round < 3; round++ {
		cf, err := r.CatFile()
		if err != nil {
			t.Fatal(err)
		}
		if n := runner.Running(); n != 1 {
			t.Fatalf("round %d: %d processes running", round, n)
		}
		kill := exec.Command("pkill", "-f", "--", regexp.QuoteMeta("--git-dir="+r.GitDir+" ")+".*cat-file")
		if out, err := kill.CombinedOutput(); err != nil {
			t.Fatalf("pkill: %v %s", err, out)
		}
		deadline := time.Now().Add(time.Minute)
		for cf.Read([][]byte{oid}, func(int, catfile.Object) error { return nil }) == nil {
			if time.Now().After(deadline) {
				t.Fatal("cat-file still reads once killed")
			}
			time.Sleep(10 * time.Millisecond)
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
	defer func() { r.Close(); <-r.Life().Done() }()
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

// Rewriting the commit-graph again and again leaves only the graphs
// something still reads mapped: a walk holding an old one goes on reading
// it, and once it lets go, the old mapping goes too.
func TestCommitGraphReleased(t *testing.T) {
	for _, split := range []bool{false, true} {
		g := gittest.Init(t)
		write := func() {
			args := []string{"commit-graph", "write", "--reachable"}
			if split {
				// Expire at once, so git deletes the layers it replaced.
				args = append(args, "--split", "--expire-time=now")
			}
			g.Git(args...)
		}
		for i := 0; i < 20; i++ {
			g.Commit("c")
		}
		write()
		r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
		if err != nil {
			t.Fatal(err)
		}
		first, err := r.CommitGraph()
		if err != nil || first == nil {
			t.Fatal(first, err)
		}
		tip := first.Lookup(hexID(t, g.Git("rev-parse", "HEAD")))
		walk := history.NewTopoWalk(history.NewSource(first, nil, 20), []uint32{tip})
		walk.Next()
		settle := func() {
			for i := 0; i < 3; i++ {
				runtime.GC()
				time.Sleep(time.Millisecond) // cleanups run on a goroutine of their own
				if _, err := r.CommitGraph(); err != nil {
					t.Fatal(err)
				}
			}
		}
		// mapped waits for want graphs mapped: the cleanups that unmap the
		// rest run when the collector and their goroutine get to them,
		// later on a busy machine.
		mapped := func(want int) int {
			deadline := time.Now().Add(time.Minute)
			for r.openGraphs() != want && time.Now().Before(deadline) {
				settle()
				time.Sleep(10 * time.Millisecond)
			}
			return r.openGraphs()
		}
		for i := 0; i < 8; i++ {
			g.Commit("more")
			write()
			if _, err := r.CommitGraph(); err != nil {
				t.Fatal(err)
			}
			settle()
		}
		if n := mapped(2); n != 2 {
			t.Errorf("split %v: %d graphs mapped, want the walked one and the current one", split, n)
		}
		n := 1
		for {
			if _, _, ok := walk.Next(); !ok {
				break
			}
			n++
		}
		if walk.Err() != nil || n != 20 {
			t.Fatalf("split %v: the old graph walked %d commits, %v", split, n, walk.Err())
		}
		walk, first = nil, nil
		if n := mapped(1); n != 1 {
			t.Errorf("split %v: %d graphs mapped once the walk let go", split, n)
		}
		if g, _ := r.CommitGraph(); g == nil || g.Len() != 28 {
			t.Fatalf("split %v: current graph %v", split, g)
		}
		r.Close()
		<-r.Life().Done()
	}
}

func hexID(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// What makes git ignore the commit-graph, done in a terminal while the
// repository is open, makes the engine ignore it too, and undone, lets it
// use the graph again.
func TestCommitGraphUsableChanges(t *testing.T) {
	up := gittest.Init(t)
	for i := 0; i < 3; i++ {
		up.Commit("c")
	}
	clone := filepath.Join(t.TempDir(), "clone")
	up.Git("clone", "-q", "file://"+up.Dir, clone)
	g := gittest.Open(t, clone)
	g.Git("commit-graph", "write", "--reachable")
	ctx := context.Background()
	r, err := Open(ctx, testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { r.Close(); <-r.Life().Done() }()
	usable := func(want bool, what string) {
		t.Helper()
		graph, err := r.CommitGraph()
		if err != nil || (graph != nil) != want || r.CommitGraphUsable() != want {
			t.Fatalf("%s: graph %v, usable %v, %v", what, graph != nil, r.CommitGraphUsable(), err)
		}
	}
	usable(true, "at open")

	a := strings.TrimSpace(g.Git("rev-parse", "HEAD~1"))
	b := strings.TrimSpace(g.Git("rev-parse", "HEAD~2"))
	g.Git("replace", a, b)
	usable(false, "replace ref")
	g.Git("pack-refs", "--all")
	usable(false, "packed replace ref")
	g.Git("replace", "-d", a)
	usable(true, "replace ref deleted")

	g.Git("config", "core.commitGraph", "false")
	usable(true, "config not read again yet")
	if err := r.ReloadConfig(ctx); err != nil {
		t.Fatal(err)
	}
	usable(false, "core.commitGraph=false")
	g.Git("config", "--unset", "core.commitGraph")
	r.ReloadConfig(ctx)
	usable(true, "core.commitGraph unset")

	grafts := filepath.Join(g.GitDir(), "info", "grafts")
	os.MkdirAll(filepath.Dir(grafts), 0o755)
	os.WriteFile(grafts, []byte(a+"\n"), 0o644)
	usable(false, "grafts")
	os.Remove(grafts)
	usable(true, "grafts removed")

	g.Git("fetch", "-q", "--depth=1", "origin")
	if _, err := os.Stat(filepath.Join(g.GitDir(), "shallow")); err != nil {
		t.Fatal("the fetch made no shallow file")
	}
	usable(false, "shallow")
	if !slices.Contains(r.Capabilities().Fallbacks(), "shallow history") {
		t.Errorf("fallbacks %q", r.Capabilities().Fallbacks())
	}
	g.Git("fetch", "-q", "--unshallow", "origin")
	usable(true, "unshallowed")
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
	defer func() { r.Close(); <-r.Life().Done() }()
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
	g.Git("config", "filter.evil.clean", "touch "+filepath.ToSlash(marker)+"; cat")
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
