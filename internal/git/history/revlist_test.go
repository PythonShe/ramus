package history

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commits"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func refTips(t testing.TB, r *gittest.Repo) [][]byte {
	var out [][]byte
	for _, h := range strings.Fields(r.Git("for-each-ref", "--format=%(objectname)")) {
		b, _ := hex.DecodeString(h)
		out = append(out, b)
	}
	return out
}

// RevList gives the commits, parents and order git rev-list does, and
// sees what the commit-graph would not: a replaced commit's grafted
// parents.
func TestRevList(t *testing.T) {
	r := gittest.Init(t)
	base := r.Commit("a")
	for i := 0; i < 3; i++ {
		r.Git("checkout", "-q", "-b", fmt.Sprintf("b%d", i), "main")
		for j := 0; j < 3; j++ {
			r.Write(fmt.Sprintf("b%d", i), fmt.Sprint(j))
			r.Commit("b")
		}
	}
	r.Git("checkout", "-q", "main")
	r.Commit("m")
	r.Git("merge", "-q", "--no-ff", "-m", "octopus", "b0", "b1", "b2")
	r.CommitAt("older than its parent", 1_000_000_000)
	orphan := r.Commit("graft me")
	r.Git("replace", "--graft", orphan, base)

	s := commits.New(20)
	var got []string
	err := RevList(context.Background(), testrun.Runner(t), r.GitDir(), refTips(t, r), s,
		func(id uint32, parents []uint32) error {
			line := hex.EncodeToString(s.OID(id))
			for _, p := range parents {
				line += " " + hex.EncodeToString(s.OID(p))
			}
			got = append(got, line)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSpace(r.Git("rev-list", "--parents", "--topo-order", "--all", "--exclude=refs/replace/*")), "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(strings.Join(got, "\n"), orphan+" "+base) {
		t.Error("graft not applied")
	}
}

func TestRevListStops(t *testing.T) {
	r := gittest.Init(t)
	for i := 0; i < 50; i++ {
		r.Commit(fmt.Sprint(i))
	}
	stop := errors.New("stop")
	n := 0
	err := RevList(context.Background(), testrun.Runner(t), r.GitDir(), refTips(t, r), commits.New(20),
		func(uint32, []uint32) error {
			n++
			if n == 3 {
				return stop
			}
			return nil
		})
	if !errors.Is(err, stop) || n != 3 {
		t.Fatalf("err %v after %d", err, n)
	}
	err = RevList(context.Background(), testrun.Runner(t), r.GitDir(), [][]byte{make([]byte, 20)}, commits.New(20),
		func(uint32, []uint32) error { return nil })
	if err == nil {
		t.Error("a missing tip is no error")
	}
}

// BenchmarkRevListKernel streams the kernel's history through git, with
// the commit-graph git uses for the part it covers (a stale graph).
func BenchmarkRevListKernel(b *testing.B) {
	dir := gittest.FixturesDir(b) + "/linux"
	r := gittest.Open(b, dir)
	head, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	for b.Loop() {
		s := commits.New(20)
		n := 0
		err := RevList(context.Background(), testrun.Runner(b), r.GitDir(), [][]byte{head}, s,
			func(uint32, []uint32) error { n++; return nil })
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(n), "commits")
		b.ReportMetric(float64(s.Bytes())/1e6, "MB")
	}
}

func TestCovered(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("a")
	r.Git("commit-graph", "write", "--reachable")
	g := openGraph(t, r.GitDir())
	if pos, ok := Covered(g, refTips(t, r)); !ok || len(pos) != 1 {
		t.Fatalf("fresh graph: %v %v", pos, ok)
	}
	r.Commit("b")
	if _, ok := Covered(g, refTips(t, r)); ok {
		t.Error("stale graph covers a new commit")
	}
	if _, ok := Covered(nil, refTips(t, r)); ok {
		t.Error("no graph covers")
	}
}

// BenchmarkTextChunkKernel loads one chunk of rows' text through cat-file:
// the cost of text appearing when the list scrolls to rows not yet loaded.
// Chunks are spread across history, so most objects are deep in packs.
func BenchmarkTextChunkKernel(b *testing.B) {
	dir := gittest.FixturesDir(b) + "/linux"
	g := openGraph(b, dir+"/.git")
	p, err := catfile.Start(context.Background(), testrun.Runner(b), dir+"/.git")
	if err != nil {
		b.Fatal(err)
	}
	defer p.Close()
	step := g.Len() / 97
	i := 0
	for b.Loop() {
		first := (i * step) % (g.Len() - TextChunkRows)
		i++
		if _, err := LoadText(p, g, first, TextChunkRows, func(row int) uint32 { return uint32(row) }); err != nil {
			b.Fatal(err)
		}
	}
}
