package fallback

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"ramus/internal/git/commits"
	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/history"
	"ramus/internal/git/repo"
	"ramus/internal/git/tree"
)

func build(t *testing.T, args ...string) *gittest.Repo {
	r := gittest.Init(t, args...)
	r.Write("a", strings.Repeat("line\n", 20))
	r.Commit("one")
	r.Git("checkout", "-q", "-b", "side")
	r.Write("b", "side\n")
	r.Commit("side")
	r.Git("checkout", "-q", "main")
	r.Git("mv", "a", "c")
	r.Commit("rename")
	r.Git("merge", "-q", "--no-ff", "-m", "merge", "side")
	r.Git("tag", "-a", "-m", "v1", "v1")
	r.Git("commit-graph", "write", "--reachable")
	return r
}

func check(t *testing.T, g *gittest.Repo, wantFallback string, hashSize int) {
	ctx := context.Background()
	runner := testrun.Runner(t)
	r, err := repo.Open(ctx, runner, g.Dir, func(repo.Location) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !strings.Contains(strings.Join(r.Caps.Fallbacks(), ","), wantFallback) {
		t.Errorf("fallbacks %q, want %q", r.Caps.Fallbacks(), wantFallback)
	}
	if r.Caps.HashSize != hashSize {
		t.Errorf("hash size %d", r.Caps.HashSize)
	}

	// Refs: natively when the engine can, else through git.
	var tips [][]byte
	if s := r.Refs(); s != nil {
		l, err := s.List()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < l.Len(); i++ {
			tips = append(tips, l.OID(i))
		}
	} else {
		for _, h := range strings.Fields(g.Git("for-each-ref", "--format=%(objectname)")) {
			b, _ := hex.DecodeString(h)
			tips = append(tips, b)
		}
	}

	// History: the graph only when every reader can use it.
	graph, err := r.CommitGraph()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := history.Covered(graph, tips); ok {
		t.Fatal("native history on a repository that goes to git")
	}
	s := commits.New(hashSize)
	var got []string
	err = history.RevList(ctx, runner, r.GitDir, tips, s, func(id uint32, parents []uint32) error {
		got = append(got, fmt.Sprintf("%x/%d", s.OID(id), len(parents)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, line := range strings.Split(strings.TrimSpace(g.Git("rev-list", "--topo-order", "--parents", "--all")), "\n") {
		f := strings.Fields(line)
		want = append(want, fmt.Sprintf("%s/%d", f[0], len(f)-1))
	}
	if strings.Join(got, " ") != strings.Join(want, " ") || len(got) != 4 {
		t.Fatalf("history\n got %v\nwant %v", got, want)
	}

	// The commit's file list and patch.
	head := s.OID(0)
	parentIDs, _ := s.Parents(0, nil)
	first := s.OID(parentIDs[0])
	raw, _, err := diff.Renames(ctx, runner, r.GitDir, hashSize, first, head)
	if err != nil || raw.Len() != 1 || raw.Status(0) != 'A' {
		t.Fatalf("merge's file list: %v %d", err, raw.Len())
	}
	rename := s.OID(parentIDs[0])
	rp, _ := s.Parents(parentIDs[0], nil)
	raw, _, err = diff.Renames(ctx, runner, r.GitDir, hashSize, s.OID(rp[0]), rename)
	if err != nil || raw.Len() != 1 || raw.Status(0) != 'R' {
		t.Fatalf("rename: %v", err)
	}
	cf, err := r.CatFile()
	if err != nil {
		t.Fatal(err)
	}
	_, o := raw.Old(0)
	_, n := raw.New(0)
	old, new, err := diff.BlobPair(cf, o, n)
	if err != nil || string(old) != string(new) {
		t.Fatalf("blob pair: %v", err)
	}
	trees := strings.Fields(g.Git("rev-parse", "HEAD~^{tree}", "HEAD~2^{tree}"))
	a, _ := hex.DecodeString(trees[1])
	b, _ := hex.DecodeString(trees[0])
	ch, err := tree.Diff(cf, hashSize, a, b)
	if err != nil || ch.Len() != 2 {
		t.Fatalf("tree diff: %v", err)
	}
}

func TestReftable(t *testing.T) {
	g := build(t, "--ref-format=reftable")
	check(t, g, "reftable refs", 20)
}

func TestSHA256(t *testing.T) {
	g := build(t, "--object-format=sha256")
	check(t, g, "SHA-256 objects", 32)
}
