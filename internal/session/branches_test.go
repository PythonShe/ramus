package session

import (
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// The refs listing carries each branch's upstream and how far apart they
// are, the tags with the commit each names, and the remotes.
func TestLoadRefInfo(t *testing.T) {
	up := gittest.Init(t)
	up.Write("a", "1")
	up.Commit("one")
	up.Git("branch", "gone")
	r := gittest.Init(t)
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("config", "remote.origin.pushurl", "file:///nowhere")
	r.Git("fetch", "-q", "origin")
	r.Git("switch", "-q", "-c", "work", "--track", "origin/main")
	r.Write("b", "2")
	r.Commit("mine")
	r.Git("switch", "-q", "-c", "old", "--track", "origin/gone")
	up.Git("branch", "-D", "gone")
	r.Git("fetch", "-q", "--prune", "origin")
	r.Git("tag", "light", "work")
	r.Git("tag", "-a", "-m", "release", "v1", "work")

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	var info *RefInfo
	s.LoadRefInfo(func(got RefInfo) { info = &got })
	m.until(t, "the refs", func() bool { return info != nil })
	if info.Err != nil {
		t.Fatal(info.Err)
	}
	branches := map[string]BranchInfo{}
	for _, b := range info.Branches {
		branches[b.Name] = b
	}
	if b := branches["refs/heads/work"]; b.Upstream != "refs/remotes/origin/main" || b.Ahead != 1 || b.Behind != 0 || b.Subject != "mine" {
		t.Fatalf("work: %+v", b)
	}
	if b := branches["refs/heads/old"]; !b.Gone {
		t.Fatalf("old: %+v", b)
	}
	work := branches["refs/heads/work"].Commit
	tags := map[string]TagInfo{}
	for _, tg := range info.Tags {
		tags[tg.Name] = tg
	}
	if tg := tags["refs/tags/light"]; tg.Annotated || tg.Commit != work {
		t.Fatalf("light: %+v", tg)
	}
	if tg := tags["refs/tags/v1"]; !tg.Annotated || tg.Commit != work || tg.Object == work || tg.Subject != "release" {
		t.Fatalf("v1: %+v", tg)
	}
	if len(info.Remotes) != 1 || info.Remotes[0] != (RemoteInfo{Name: "origin", URL: up.Dir, PushURL: "file:///nowhere"}) {
		t.Fatalf("remotes: %+v", info.Remotes)
	}
}

// Branches merged, rebased onto, or squashed into the base are found, and
// so is one whose upstream went; work not in the base is not.
func TestFindCleanup(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	branch := func(name, file string) {
		r.Git("switch", "-q", "-c", name, "main")
		r.Write(file, name+"\n")
		r.Commit(name + " one")
		r.Write(file, name+"\nmore\n")
		r.Commit(name + " two")
		r.Git("switch", "-q", "main")
	}
	branch("merged", "m")
	r.Git("merge", "-q", "--no-ff", "-m", "merge", "merged")
	branch("rebased", "r")
	r.Git("cherry-pick", "main..rebased")
	branch("squashed", "s")
	r.Git("merge", "-q", "--squash", "squashed")
	r.Commit("squash")
	branch("open", "o")
	r.Write("x", "later\n")
	r.Commit("later on main")

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	var info *RefInfo
	s.LoadRefInfo(func(got RefInfo) { info = &got })
	m.until(t, "the refs", func() bool { return info != nil })
	for i := range info.Branches {
		if info.Branches[i].Name == "refs/heads/open" {
			info.Branches[i].Gone = true
		}
	}
	var got map[string]Why
	s.FindCleanup("refs/heads/main", "refs/heads/main", info.Branches, func(c []Candidate, err error) {
		if err != nil {
			t.Error(err)
		}
		got = map[string]Why{}
		for _, x := range c {
			got[x.Branch.Name] = x.Why
		}
	})
	m.until(t, "the candidates", func() bool { return got != nil })
	want := map[string]Why{
		"refs/heads/merged":   Merged,
		"refs/heads/rebased":  RebaseMerged,
		"refs/heads/squashed": SquashMerged,
		"refs/heads/open":     UpstreamGone,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %b, want %b", k, got[k], v)
		}
	}
}
