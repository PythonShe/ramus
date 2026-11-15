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

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// startTips are the tips history starts from, as the app passes them:
// HEAD, then every ref, an annotated tag peeled; a tag may name a tree.
func startTips(t testing.TB, r *gittest.Repo) [][]byte {
	t.Helper()
	var out [][]byte
	add := func(h string) {
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	add(strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	for _, line := range strings.Split(strings.TrimSpace(r.Git("for-each-ref", "--format=%(objectname) %(objecttype) %(*objectname)")), "\n") {
		f := strings.Fields(line)
		if f[1] == "tag" && len(f) > 2 {
			add(f[2])
		} else {
			add(f[0])
		}
	}
	return out
}

// gitOrder is git's --topo-order from tips, given in that order.
func gitOrder(t testing.TB, r *gittest.Repo, tips [][]byte) []string {
	t.Helper()
	var in strings.Builder
	for _, tip := range tips {
		fmt.Fprintf(&in, "%x\n", tip)
	}
	out, err := r.Run([]byte(in.String()), "rev-list", "--topo-order", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(out)
}

func startCat(t testing.TB, r *gittest.Repo) *catfile.Process {
	t.Helper()
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// walkAll orders history from src and returns it as hex, with the walk.
func walkAll(t testing.TB, src *Source, tips []uint32) ([]string, *TopoWalk) {
	t.Helper()
	w := NewTopoWalk(src, tips)
	var out []string
	for {
		id, _, ok := w.Next()
		if !ok {
			break
		}
		out = append(out, hex.EncodeToString(src.OID(id)))
	}
	if w.Err() != nil {
		t.Fatal(w.Err())
	}
	return out, w
}

func sameOrder(t testing.TB, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") == strings.Join(want, " ") {
		return
	}
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			t.Fatalf("%s: %d rows, git %d; first difference at row %d: %s, git %s", what, len(got), len(want), i, got[i], want[i])
		}
	}
	t.Fatalf("%s: %d rows, git %d", what, len(got), len(want))
}

// stalePast builds a history, writes its commit-graph, then makes
// commits the graph does not have: on top, merges among themselves and
// of old branches, an octopus, an orphan, ties in time and a commit older
// than its parent, and tags of a new commit and of a tree.
func stalePast(t *testing.T) *gittest.Repo {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("a")
	for i := range 3 {
		r.Git("checkout", "-q", "-b", fmt.Sprintf("b%d", i), "main")
		for j := range 3 {
			r.Write(fmt.Sprintf("b%d", i), fmt.Sprint(j))
			r.Commit(fmt.Sprintf("b%d %d", i, j))
		}
	}
	r.Git("checkout", "-q", "main")
	for j := range 4 {
		r.Write("m", fmt.Sprint(j))
		r.Commit(fmt.Sprintf("main %d", j))
	}
	r.Env = []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", r.Tick())}
	r.Git("merge", "-q", "--no-ff", "-m", "merge b0", "b0")
	r.Env = nil
	r.Git("tag", "-a", "-m", "old", "v1", "main~2")
	r.Git("commit-graph", "write", "--reachable", "--split")

	// Made since the graph was written.
	r.Write("m", "new 1")
	r.Commit("new on main")
	r.Git("checkout", "-q", "-b", "feat", "main~4")
	for j := range 3 {
		r.Write("f", fmt.Sprint(j))
		r.Commit(fmt.Sprintf("feat %d", j))
	}
	r.Git("checkout", "-q", "main")
	r.Env = []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", r.Tick())}
	r.Git("merge", "-q", "--no-ff", "-m", "merge feat", "feat")
	r.Git("merge", "-q", "--no-ff", "-m", "merge old b1", "b1")
	r.Env = nil
	for _, b := range []string{"o1", "o2"} {
		r.Git("checkout", "-q", "-b", b, "main~1")
		r.Write(b, b)
		r.Commit(b)
	}
	r.Git("checkout", "-q", "main")
	r.Env = []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", r.Tick())}
	r.Git("merge", "-q", "--no-ff", "-m", "octopus", "o1", "o2")
	r.Env = nil
	// Two tips at the very same time, and one older than its parent.
	same := r.Tick()
	for _, b := range []string{"same1", "same2"} {
		r.Git("checkout", "-q", "-b", b, "main")
		r.Write(b, b)
		r.CommitAt(b, same)
	}
	r.Git("checkout", "-q", "-b", "skew", "main")
	r.Write("skew", "1")
	r.CommitAt("older than its parent", 1_000_000_000)
	r.Git("checkout", "-q", "--orphan", "orphan")
	r.Write("o", "1")
	r.Commit("orphan root")
	r.Write("o", "2")
	r.Commit("orphan top")
	r.Git("checkout", "-q", "main")
	r.Git("tag", "-a", "-m", "new", "v2", "feat")
	r.Git("tag", "tree", "main^{tree}")
	return r
}

// Commits made since the commit-graph was written are read from their
// objects, and history comes out exactly as git orders it: merges among
// them, merges of branches the graph has, an octopus and an orphan.
func TestSourceOrdersPastTheGraph(t *testing.T) {
	r := stalePast(t)
	g := openGraph(t, r.GitDir())
	tips := startTips(t, r)
	if _, ok := Covered(g, tips[:1]); ok {
		t.Fatal("the graph has the new commits")
	}
	src := NewSource(g, nil, 20)
	ids, err := src.Resolve(context.Background(), startCat(t, r), tips, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(tips)-1 {
		t.Fatalf("%d of %d tips are commits, want all but the tree", len(ids), len(tips))
	}
	newOnes := 0
	for _, h := range strings.Fields(r.Git("rev-list", "--all")) {
		if b, _ := hex.DecodeString(h); g.Lookup(b) == commitgraph.None {
			newOnes++
		}
	}
	if src.Read() != newOnes || newOnes < 10 {
		t.Errorf("read %d commits, %d are new", src.Read(), newOnes)
	}
	got, _ := walkAll(t, src, ids)
	sameOrder(t, "past the graph", got, gitOrder(t, r, tips))

	// Reading them again reads nothing.
	if _, err := src.Resolve(context.Background(), startCat(t, r), tips, 0); err != nil {
		t.Fatal(err)
	}

	// No more is read than the limit allows.
	if _, err := NewSource(g, nil, 20).Resolve(context.Background(), startCat(t, r), tips, 3); err != ErrTooManyRead {
		t.Fatalf("past the limit: %v", err)
	}
}

// kernelClone is a bare clone of the kernel fixture sharing its objects,
// with its commit-graph linked in, for commits to be made in it.
func kernelClone(t *testing.T) *gittest.Repo {
	t.Helper()
	fix := filepath.Join(gittest.FixturesDir(t), "linux")
	if _, err := os.Stat(fix); err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(t.TempDir(), "linux.git")
	gittest.Open(t, filepath.Dir(dir)).Git("clone", "-q", "--bare", "--shared", fix, dir)
	from := filepath.Join(fix, ".git", "objects", "info", "commit-graphs")
	to := filepath.Join(dir, "objects", "info", "commit-graphs")
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(from)
	if err != nil {
		t.Skip(err)
	}
	for _, e := range ents {
		if err := os.Link(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
			t.Skip(err)
		}
	}
	return gittest.Open(t, dir)
}

// TestPastTheGraphKernel orders the kernel natively with commits on top
// that its commit-graph lacks, merges among them and of old tags, as git
// does: go test -run PastTheGraphKernel with RAMUS_FIXTURES set.
func TestPastTheGraphKernel(t *testing.T) {
	r := kernelClone(t)
	head := strings.TrimSpace(r.Git("rev-parse", "master"))
	old := strings.TrimSpace(r.Git("rev-parse", "v6.0^{commit}"))
	older := strings.TrimSpace(r.Git("rev-parse", "v5.0^{commit}"))
	var b strings.Builder
	when := 2_000_000_000
	marks := 0
	commit := func(ref, msg string, parents ...string) string {
		when += 60
		marks++
		fmt.Fprintf(&b, "commit %s\nmark :%d\ncommitter T <t@example.com> %d +0000\ndata %d\n%s\n", ref, marks, when, len(msg), msg)
		for i, p := range parents {
			if i == 0 {
				fmt.Fprintf(&b, "from %s\n", p)
			} else {
				fmt.Fprintf(&b, "merge %s\n", p)
			}
		}
		b.WriteString("\n")
		return fmt.Sprintf(":%d", marks)
	}
	m := commit("refs/heads/master", "on top 1", head)
	m = commit("refs/heads/master", "on top 2", m)
	tp := commit("refs/heads/topic", "topic 1", old)
	tp = commit("refs/heads/topic", "topic 2", tp)
	m = commit("refs/heads/master", "merge topic", m, tp)
	m = commit("refs/heads/master", "merge v5.0", m, older)
	commit("refs/heads/master", "on top 3", m)
	if _, err := r.Run([]byte(b.String()), "fast-import", "--quiet"); err != nil {
		t.Fatal(err)
	}
	g := openGraph(t, r.Dir)
	tips := startTips(t, r)
	src := NewSource(g, nil, 20)
	start := time.Now()
	ids, err := src.Resolve(context.Background(), startCat(t, r), tips, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if src.Read() != 7 {
		t.Fatalf("read %d commits, 7 are new", src.Read())
	}
	got, _ := walkAll(t, src, ids)
	took := time.Since(start)
	sameOrder(t, "the kernel past its graph", got, gitOrder(t, r, tips))
	t.Logf("%d rows as git orders them, in %v", len(got), took)
}
