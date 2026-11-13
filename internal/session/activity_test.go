package session

import (
	"context"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/repo"
)

// The key holds while nothing the records come from changes, and the
// records count every commit any branch or tag reaches.
func TestActivity(t *testing.T) {
	r := gittest.Init(t)
	for i := range 4 {
		r.Write("a", string(rune('a'+i)))
		r.Commit("c")
	}
	r.Git("tag", "-a", "-m", "t", "v1", "HEAD~1")
	r.Git("checkout", "-q", "-b", "side", "HEAD~2")
	r.Write("b", "1")
	r.Commit("side")
	ctx := context.Background()
	open := func() *repo.Repository {
		t.Helper()
		rp, err := repo.Open(ctx, testrun.Runner(t), r.Dir, func(repo.Location) bool { return false })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rp.Close(); <-rp.Life().Done() })
		return rp
	}
	rp := open()
	src, err := FindActivity(ctx, rp)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ReadActivity(ctx, rp, src, nil)
	if err != nil || rec.Len() != 5 {
		t.Fatalf("%v, %d commits", err, rec.Len())
	}
	if again, _ := FindActivity(ctx, open()); again.Key != src.Key {
		t.Error("the key changed with nothing changed")
	}
	r.Write(".mailmap", "Someone <test@example.com>\n")
	r.Commit("mailmap")
	r.Git("checkout", "-q", "main")
	after, _ := FindActivity(ctx, open())
	if after.Key == src.Key {
		t.Error("the key held over a new commit")
	}
	if size, err := RepoSize(ctx, rp); err != nil || size <= 0 {
		t.Errorf("size %d, %v", size, err)
	}
}
