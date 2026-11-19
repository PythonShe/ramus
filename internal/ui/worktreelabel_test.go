package ui

import (
	"testing"

	"ramus/internal/git/repo"
)

func TestWorktreeLabelIsOneMessage(t *testing.T) {
	for _, c := range []struct {
		w    repo.Worktree
		want string
	}{
		{repo.Worktree{Path: "/r/main", Branch: "refs/heads/main"}, "main  main"},
		{repo.Worktree{Path: "/r/wt", Detached: true}, "wt  detached"},
		{repo.Worktree{Path: "/r/wt", Branch: "refs/heads/feature", Locked: true}, "wt  feature  locked"},
		{repo.Worktree{Path: "/r/wt", Detached: true, Prunable: true, Locked: true}, "wt  detached  folder missing"},
		{repo.Worktree{Path: "/r/bare"}, "bare"},
		{repo.Worktree{Path: "/r/wt", Prunable: true}, "wt  folder missing"},
	} {
		if got := worktreeLabel(c.w); got != c.want {
			t.Errorf("worktreeLabel(%+v) = %q, want %q", c.w, got, c.want)
		}
	}
}
