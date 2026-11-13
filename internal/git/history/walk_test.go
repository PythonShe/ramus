package history

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
	"ramus/internal/git/lanes"
)

func openGraph(t testing.TB, gitDir string) *commitgraph.Graph {
	t.Helper()
	g, err := commitgraph.Open(filepath.Join(gitDir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func tips(t testing.TB, r *gittest.Repo, g *commitgraph.Graph) []uint32 {
	var ps []uint32
	for _, line := range strings.Split(strings.TrimSpace(r.Git("for-each-ref", "--format=%(objectname) %(objecttype) %(*objectname)")), "\n") {
		f := strings.Fields(line)
		oid := f[0]
		if f[1] == "tag" {
			if len(f) < 3 {
				continue
			}
			oid = f[2]
		} else if f[1] != "commit" {
			continue
		}
		b, _ := hex.DecodeString(oid)
		if p := g.Lookup(b); p != commitgraph.None {
			ps = append(ps, p)
		}
	}
	return ps
}

// The walk visits the same commits as git rev-list --all, each after all of
// its children.
func TestWalkOrder(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("a")
	for i := 0; i < 4; i++ {
		r.Git("checkout", "-q", "-b", fmt.Sprintf("b%d", i), "main")
		for j := 0; j < 3; j++ {
			r.Write(fmt.Sprintf("b%d", i), fmt.Sprint(j))
			r.Commit("b")
		}
	}
	r.Git("checkout", "-q", "main")
	r.Write("m", "x")
	r.Commit("m")
	r.Env = []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", r.Tick())}
	r.Git("merge", "-q", "--no-ff", "-m", "merge", "b0", "b1")
	r.Env = nil
	r.CommitAt("older than its parent", 1_000_000_000)
	r.Git("commit-graph", "write", "--reachable")

	g := openGraph(t, r.GitDir())
	for _, w := range []walker{NewWalk(g, tips(t, r, g)), NewTopoWalk(g, tips(t, r, g))} {
		checkWalk(t, r, g, w)
	}
	// git's own --topo-order is the oracle for the order itself.
	w := NewTopoWalk(g, tips(t, r, g))
	var got []string
	for {
		pos, _, ok := w.Next()
		if !ok {
			break
		}
		got = append(got, hex.EncodeToString(g.OID(pos)))
	}
	if want := strings.Fields(r.Git("rev-list", "--topo-order", "--all")); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("topo order\n got %v\nwant %v", got, want)
	}
}

type walker interface {
	Next() (uint32, []uint32, bool)
	Err() error
}

func checkWalk(t *testing.T, r *gittest.Repo, g *commitgraph.Graph, w walker) {
	t.Helper()
	shown := map[uint32]bool{}
	children := map[uint32][]uint32{}
	var order []uint32
	for {
		pos, ps, ok := w.Next()
		if !ok {
			break
		}
		order = append(order, pos)
		for _, p := range ps {
			children[p] = append(children[p], pos)
		}
	}
	if w.Err() != nil {
		t.Fatal(w.Err())
	}
	want := strings.Fields(r.Git("rev-list", "--all"))
	if len(order) != len(want) {
		t.Fatalf("walk visited %d commits, rev-list %d", len(order), len(want))
	}
	for _, pos := range order {
		for _, c := range children[pos] {
			if !shown[c] {
				t.Fatalf("%x shown before its child %x", g.OID(pos), g.OID(c))
			}
		}
		shown[pos] = true
	}
}

// BenchmarkKernel walks and lays out the whole history of the Linux kernel
// fixture: go test -run '^$' -bench Kernel with RAMUS_FIXTURES set.
func BenchmarkKernel(b *testing.B) {
	dir := filepath.Join(gittest.FixturesDir(b), "linux")
	if _, err := os.Stat(dir); err != nil {
		b.Skip(err)
	}
	r := gittest.Open(b, dir)
	g := openGraph(b, filepath.Join(dir, ".git"))
	ts := tips(b, r, g)
	for b.Loop() {
		start := time.Now()
		w := walker(NewTopoWalk(g, ts))
		if os.Getenv("WALK") == "date" {
			w = NewWalk(g, ts)
		}
		var l lanes.Layout
		first := time.Duration(0)
		for {
			pos, ps, ok := w.Next()
			if !ok {
				break
			}
			l.Append(pos, ps)
			if l.Len() == 100 {
				first = time.Since(start)
			}
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		b.ReportMetric(float64(first.Microseconds()), "µs-to-100-rows")
		b.ReportMetric(float64(l.Len()), "rows")
		b.ReportMetric(float64(l.Width()), "columns")
		b.ReportMetric(float64(l.Bytes())/(1<<20), "layout-MB")
		b.ReportMetric(float64(time.Since(start).Milliseconds()), "ms-full")
	}
}
