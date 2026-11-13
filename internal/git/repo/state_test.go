package repo

import (
	"testing"

	"ramus/internal/git/gittest"
)

func TestInProgress(t *testing.T) {
	r := gittest.Init(t)
	r.Write("f", "base\n")
	base := r.Commit("base")
	r.Git("checkout", "-q", "-b", "other")
	r.Write("f", "other\n")
	other := r.Commit("other")
	r.Git("checkout", "-q", "main")
	r.Write("f", "main\n")
	r.Commit("main")
	gd := r.GitDir()
	check := func(want Operation) {
		t.Helper()
		if got := InProgress(gd); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	check(NoOperation)
	r.Run(nil, "merge", "other")
	check(Merge)
	r.Git("merge", "--abort")
	r.Run(nil, "cherry-pick", other)
	check(CherryPick)
	r.Git("cherry-pick", "--abort")
	r.Run(nil, "revert", "--no-edit", base) // f changed since: conflicts
	check(Revert)
	r.Git("revert", "--abort")
	r.Run(nil, "rebase", "other")
	check(Rebase)
	r.Git("rebase", "--abort")
	patch := r.Git("format-patch", "-1", "--stdout", other)
	r.Run([]byte(patch), "am")
	check(Am)
	r.Git("am", "--abort")
	r.Git("bisect", "start")
	check(Bisect)
	r.Git("bisect", "reset")
	check(NoOperation)
}
