package session

import (
	"context"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/repo"
)

// The brief finds the branch HEAD is on where the engine does not read
// the refs, through git, as where it does.
func TestBriefBranchWithoutNativeRefs(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	ctx := context.Background()
	rp, err := repo.Open(ctx, testrun.Runner(t), r.Dir, func(repo.Location) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rp.Close(); <-rp.Life().Done() })
	if got := briefBranch(ctx, rp); got != "refs/heads/main" {
		t.Fatalf("native: %q", got)
	}
	// A ref storage the engine does not read: Refs is nil.
	rp.Caps.RefStorage = "other"
	if rp.Refs() != nil {
		t.Fatal("refs read natively")
	}
	if got := briefBranch(ctx, rp); got != "refs/heads/main" {
		t.Fatalf("through git: %q", got)
	}
}
