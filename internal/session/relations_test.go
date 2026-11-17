package session

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// wantRelations is what git says of commit c: the refs that contain it, in
// the order CommitRelations lists them, and its children, newest first.
func wantRelations(r *gittest.Repo, c string) (refs, children []string) {
	defer func() {
		// --children lists them as it found them.
		time := map[string]string{}
		for _, k := range children {
			time[k] = r.Git("log", "-1", "--format=%ct", k)
		}
		slices.SortStableFunc(children, func(a, b string) int { return cmp.Compare(time[b], time[a]) })
	}()
	for _, name := range strings.Fields(r.Git("for-each-ref", "--contains", c, "--format=%(refname)", "refs/heads/", "refs/tags/", "refs/remotes/")) {
		if name != "refs/remotes/origin/HEAD" {
			refs = append(refs, name)
		}
	}
	slices.SortStableFunc(refs, func(a, b string) int { return cmp.Compare(refKind(a), refKind(b)) })
	for line := range strings.SplitSeq(strings.TrimSpace(r.Git("rev-list", "--children", "HEAD", "--branches", "--tags", "--remotes")), "\n") {
		if f := strings.Fields(line); f[0] == c {
			children = f[1:]
		}
	}
	return refs, children
}

// The refs that contain a commit and its children come out as git tells
// them, natively from the commit-graph and through git without one, and
// are kept until the refs move.
func TestCommitRelations(t *testing.T) {
	runner, logged := loggedRunner(t)
	r := gittest.Init(t)
	root := r.Commit("root")
	for i := range 120 {
		r.Git("tag", fmt.Sprintf("many%03d", i), root)
	}
	r.Commit("two")
	r.Git("checkout", "-q", "-b", "side")
	r.Commit("side one")
	r.Git("checkout", "-q", "main")
	r.Commit("three")
	r.Env = []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", r.Tick())}
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	r.Env = nil
	r.Git("tag", "-a", "-m", "annotated", "v1", "main~1")
	r.Git("tag", "-a", "-m", "outer", "v1-outer", "v1")
	r.Git("tag", "light", "side")
	r.Git("tag", "tree", "main^{tree}")
	r.Git("update-ref", "refs/remotes/origin/main", "main~2")
	r.Git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	r.Git("checkout", "-q", "--detach", "main")
	r.Commit("detached")
	r.Write("wip", "1")
	r.Git("stash", "-q", "--include-untracked")

	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })
	m.settle(t, s)

	load := func(c string) (rel CommitRelations, atOnce bool) {
		t.Helper()
		got := 0
		s.LoadCommitRelations(mustHex(t, c), func(cr CommitRelations) { rel = cr; got++ })
		atOnce = got == 1
		m.until(t, "the relations of "+c, func() bool { return got > 0 })
		m.settle(t, s)
		if got != 1 {
			t.Fatalf("%s: done ran %d times", c, got)
		}
		return rel, atOnce
	}
	check := func(what string) {
		t.Helper()
		for _, c := range strings.Fields(r.Git("rev-list", "HEAD", "--branches", "--tags", "--remotes")) {
			rel, _ := load(c)
			if rel.Err != nil {
				t.Fatalf("%s %s: %v", what, c, rel.Err)
			}
			refs, children := wantRelations(r, c)
			if len(refs) > maxRelationRefs {
				refs, rel.More = refs[:maxRelationRefs], rel.More-(len(refs)-maxRelationRefs)
			}
			if !slices.Equal(rel.Refs, refs) || rel.More != 0 {
				t.Errorf("%s %s: refs %v more %d, git %v", what, c, rel.Refs, rel.More, refs)
			}
			var kids []string
			for _, k := range rel.Children {
				kids = append(kids, hex.EncodeToString(k))
			}
			if !slices.Equal(kids, children) {
				t.Errorf("%s %s: children %v, git %v", what, c, kids, children)
			}
		}
	}
	contains := func() int {
		n := 0
		for _, l := range logged() {
			if strings.Contains(l, "--contains") {
				n++
			}
		}
		return n
	}

	check("without a graph")
	if contains() == 0 {
		t.Fatal("git was not asked without a commit-graph")
	}
	rel, _ := load(root)
	if all, _ := wantRelations(r, root); len(rel.Refs) != maxRelationRefs || rel.More != len(all)-maxRelationRefs {
		t.Fatalf("the root: %d refs listed, %d more", len(rel.Refs), rel.More)
	}

	// The relations found are kept for the refs as they are.
	if _, atOnce := load(root); !atOnce {
		t.Fatal("the root's relations again were not kept")
	}
	before := s.State.Refs
	r.Git("commit-graph", "write", "--reachable")
	// The tags read loose so far come peeled from packed-refs now.
	r.Git("pack-refs", "--all")
	r.Git("branch", "late", root)
	m.until(t, "the new branch", func() bool { return s.State.Refs != before })
	m.settle(t, s)
	asked := contains()
	rel, atOnce := load(root)
	if atOnce || !slices.Contains(rel.Refs, "refs/heads/late") {
		t.Fatalf("after a new branch: at once %v, refs %v", atOnce, rel.Refs)
	}
	check("from the commit-graph")
	if contains() != asked {
		t.Fatal("git was asked with a commit-graph")
	}
}
