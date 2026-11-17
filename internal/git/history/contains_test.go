package history

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// relationsRepo is stalePast with remote branches, a lightweight tag, a
// tag of a tag and a branch at an old commit added.
func relationsRepo(t *testing.T) *gittest.Repo {
	r := stalePast(t)
	r.Git("update-ref", "refs/remotes/origin/main", "main~3")
	r.Git("update-ref", "refs/remotes/origin/feat", "feat~1")
	r.Git("tag", "light", "b2~1")
	r.Git("tag", "-a", "-m", "outer", "outer", "v1")
	r.Git("branch", "old", "main~5")
	return r
}

// namedTips are the branches, remote branches and tags of r, each peeled to
// what it names in the end, a tag maybe to a tree.
func namedTips(t testing.TB, r *gittest.Repo) (names []string, tips [][]byte) {
	t.Helper()
	for _, name := range strings.Fields(r.Git("for-each-ref", "--format=%(refname)", "refs/heads/", "refs/remotes/", "refs/tags/")) {
		b, err := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", name+"^{}")))
		if err != nil {
			t.Fatal(err)
		}
		names, tips = append(names, name), append(tips, b)
	}
	return names, tips
}

// gitChildren is git's children of every commit, by hex id, from
// rev-list --all --children.
func gitChildren(r *gittest.Repo) map[string][]string {
	out := map[string][]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(r.Git("rev-list", "--all", "--children")), "\n") {
		f := strings.Fields(line)
		out[f[0]] = f[1:]
	}
	return out
}

func hexes(ids [][]byte) []string {
	var out []string
	for _, id := range ids {
		out = append(out, hex.EncodeToString(id))
	}
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// relationsOracle is what git says of every commit of a repository: the
// refs that contain it, by for-each-ref --contains, and its children, by
// rev-list --children.
type relationsOracle struct {
	names    []string
	tips     [][]byte // by names, then HEAD
	commits  [][]byte // the tips that are commits
	all      []string
	refs     map[string][]string
	children map[string][]string
	time     map[string]int64
}

func newRelationsOracle(t *testing.T, r *gittest.Repo) *relationsOracle {
	o := &relationsOracle{refs: map[string][]string{}, children: gitChildren(r), time: map[string]int64{}}
	o.names, o.tips = namedTips(t, r)
	head, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	o.tips = append(o.tips, head)
	for _, tip := range o.tips {
		if r.Git("cat-file", "-t", hex.EncodeToString(tip)) == "commit\n" {
			o.commits = append(o.commits, tip)
		}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(r.Git("log", "--all", "--format=%H %ct")), "\n") {
		c, ts, _ := strings.Cut(line, " ")
		o.time[c], _ = strconv.ParseInt(ts, 10, 64)
		o.all = append(o.all, c)
		o.refs[c] = strings.Fields(r.Git("for-each-ref", "--contains", c, "--format=%(refname)", "refs/heads/", "refs/remotes/", "refs/tags/"))
	}
	return o
}

// check compares what RelationsOf finds for every commit with git, the
// children newest first; newSource makes the source each is walked in.
func (o *relationsOracle) check(t *testing.T, r *gittest.Repo, newSource func() *Source) {
	t.Helper()
	cat := startCat(t, r)
	for _, c := range o.all {
		target, _ := hex.DecodeString(c)
		rel, err := RelationsOf(context.Background(), newSource(), cat, target, o.tips, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for i, ok := range rel.Reach[:len(o.names)] {
			if ok {
				got = append(got, o.names[i])
			}
		}
		if !slices.Equal(sorted(got), sorted(o.refs[c])) {
			t.Errorf("%s: contained by %v, git %v", c, got, o.refs[c])
		}
		kids := hexes(rel.Children)
		if !slices.Equal(sorted(kids), sorted(o.children[c])) {
			t.Errorf("%s: children %v, git %v", c, kids, o.children[c])
		}
		for i := 1; i < len(kids); i++ {
			if o.time[kids[i]] > o.time[kids[i-1]] {
				t.Errorf("%s: children %v not newest first", c, kids)
			}
		}
	}
}

// checkGit compares what the fallbacks through git find with git.
func (o *relationsOracle) checkGit(t *testing.T, r *gittest.Repo) {
	ctx := context.Background()
	for _, c := range o.all {
		target, _ := hex.DecodeString(c)
		refs, err := RefsContainingGit(ctx, testrun.Runner(t), r.GitDir(), target)
		if err != nil || !slices.Equal(sorted(refs), sorted(o.refs[c])) {
			t.Errorf("%s: contained by %v, %v; git %v", c, refs, err, o.refs[c])
		}
		kids, err := ChildrenGit(ctx, testrun.Runner(t), r.GitDir(), target, o.commits)
		if err != nil || !slices.Equal(sorted(hexes(kids)), sorted(o.children[c])) {
			t.Errorf("%s: children %v, %v; git %v", c, hexes(kids), err, o.children[c])
		}
	}
}

func removeGraph(t *testing.T, r *gittest.Repo) {
	info := filepath.Join(r.GitDir(), "objects", "info")
	for _, p := range []string{filepath.Join(info, "commit-graph"), filepath.Join(info, "commit-graphs")} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
}

// Which refs contain a commit and what its children are come out as git
// tells them: from a commit-graph whole, one the newest commits are
// missing from, one without generation numbers, and none.
func TestRelationsOf(t *testing.T) {
	r := relationsRepo(t)
	o := newRelationsOracle(t, r)
	t.Run("git", func(t *testing.T) { o.checkGit(t, r) })
	t.Run("stale graph", func(t *testing.T) {
		g := openGraph(t, r.GitDir())
		o.check(t, r, func() *Source { return NewSource(g, nil, 20) })
	})
	t.Run("no graph", func(t *testing.T) {
		o.check(t, r, func() *Source { return NewSource(nil, nil, 20) })
	})
	removeGraph(t, r)
	r.Git("commit-graph", "write", "--reachable")
	t.Run("whole graph", func(t *testing.T) {
		g := openGraph(t, r.GitDir())
		o.check(t, r, func() *Source { return NewSource(g, nil, 20) })
	})
	removeGraph(t, r)
	r.WriteGraphWithoutGenerations()
	t.Run("no generations", func(t *testing.T) {
		g := openGraph(t, r.GitDir())
		if g.HasGenerations() {
			t.Fatal("the graph has generations")
		}
		o.check(t, r, func() *Source { return NewSource(g, nil, 20) })
	})
}

// A commit the tips do not reach has no relations, and a walk past the
// limit of commits to read says so.
func TestRelationsOfUnreached(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("a")
	lost := r.Commit("b")
	r.Git("reset", "-q", "--hard", "HEAD~1")
	r.Commit("c")
	r.Commit("d")
	head, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	target, _ := hex.DecodeString(lost)
	rel, err := RelationsOf(context.Background(), NewSource(nil, nil, 20), startCat(t, r), target, [][]byte{head}, 1000)
	if err != nil || rel.Reach[0] || len(rel.Children) != 0 {
		t.Fatalf("%+v %v", rel, err)
	}
	_, err = RelationsOf(context.Background(), NewSource(nil, nil, 20), startCat(t, r), target, [][]byte{head}, 2)
	if err != ErrTooManyRead {
		t.Fatalf("past the limit: %v", err)
	}
}

// On the kernel, the tags containing an old commit, and its children, take
// one walk of the history above it.
func TestRelationsKernel(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "linux")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	r := gittest.Open(t, dir)
	g := openGraph(t, filepath.Join(dir, ".git"))
	if g == nil || !g.HasGenerations() {
		t.Skip("no commit-graph with generations")
	}
	names, tips := namedTips(t, r)
	cat := startCat(t, r)
	for _, rev := range []string{"v6.6^{commit}", "v5.15^{commit}", "v2.6.12^{commit}"} {
		target, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", rev)))
		if g.Lookup(target) == commitgraph.None {
			t.Skip("the graph lacks ", rev)
		}
		t0 := time.Now()
		rel, err := RelationsOf(context.Background(), NewSource(g, nil, 20), cat, target, tips, 1<<17)
		took := time.Since(t0)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, ok := range rel.Reach {
			if ok {
				n++
			}
		}
		want := strings.Fields(r.Git("for-each-ref", "--contains", hex.EncodeToString(target), "--format=%(refname)", "refs/heads/", "refs/remotes/", "refs/tags/"))
		if n != len(want) {
			t.Errorf("%s: %d of %d refs contain it, git %d", rev, n, len(names), len(want))
		}
		t.Logf("%s: %d refs, %d children in %v", rev, n, len(rel.Children), took)
	}
}
