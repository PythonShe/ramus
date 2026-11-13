package history

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func gitCount(t testing.TB, r *gittest.Repo, a, b string) (int, int) {
	f := strings.Fields(r.Git("rev-list", "--left-right", "--count", a+"..."+b))
	var x, y int
	fmt.Sscan(f[0], &x)
	fmt.Sscan(f[1], &y)
	return x, y
}

func pos(t testing.TB, r *gittest.Repo, g *commitgraph.Graph, rev string) uint32 {
	b, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", rev)))
	p := g.Lookup(b)
	if p == commitgraph.None {
		t.Fatalf("%s not in the graph", rev)
	}
	return p
}

func TestAheadBehind(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("base")
	r.Git("branch", "side")
	for i := 0; i < 5; i++ {
		r.Commit(fmt.Sprint("main ", i))
	}
	r.Git("checkout", "-q", "side")
	for i := 0; i < 3; i++ {
		r.Commit(fmt.Sprint("side ", i))
	}
	r.Git("merge", "-q", "--no-ff", "-m", "merge main", "main")
	r.Commit("after merge")
	r.Git("checkout", "-q", "main")
	r.Commit("main again")
	r.CommitAt("clock skew", 1_000_000_000)
	r.Git("commit-graph", "write", "--reachable")
	g := openGraph(t, r.GitDir())
	for _, pair := range [][2]string{{"main", "side"}, {"side", "main"}, {"main", "main"}, {"main~3", "side"}, {"side~1", "side"}} {
		ah, bh, err := AheadBehind(g, pos(t, r, g, pair[0]), pos(t, r, g, pair[1]))
		if err != nil {
			t.Fatal(err)
		}
		wa, wb := gitCount(t, r, pair[0], pair[1])
		if ah != wa || bh != wb {
			t.Errorf("%s...%s: %d %d, git %d %d", pair[0], pair[1], ah, bh, wa, wb)
		}
	}
	a, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "main")))
	b, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "side")))
	ah, bh, err := AheadBehindGit(context.Background(), testrun.Runner(t), r.GitDir(), a, b)
	if wa, wb := gitCount(t, r, "main", "side"); err != nil || ah != wa || bh != wb {
		t.Errorf("git: %d %d %v", ah, bh, err)
	}
}

func TestAheadBehindKernel(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "linux")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	r := gittest.Open(t, dir)
	g := openGraph(t, filepath.Join(dir, ".git"))
	for _, pair := range [][2]string{{"v6.7", "v6.6"}, {"v6.6", "v5.15"}} {
		t0 := time.Now()
		ah, bh, err := AheadBehind(g, pos(t, r, g, pair[0]+"^{commit}"), pos(t, r, g, pair[1]+"^{commit}"))
		took := time.Since(t0)
		if err != nil {
			t.Fatal(err)
		}
		wa, wb := gitCount(t, r, pair[0], pair[1])
		if ah != wa || bh != wb {
			t.Errorf("%s...%s: %d %d, git %d %d", pair[0], pair[1], ah, bh, wa, wb)
		}
		t.Logf("%s...%s: %d ahead, %d behind in %v", pair[0], pair[1], ah, bh, took)
	}
}
