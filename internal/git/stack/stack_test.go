package stack

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestFind(t *testing.T) {
	r := gittest.Init(t)
	r.Write("f", "0")
	r.Commit("base")
	work := func(branch, from string) {
		r.Git("checkout", "-q", "-b", branch, from)
		for i := range 2 {
			r.Write(branch, fmt.Sprint(i))
			r.Commit(branch + fmt.Sprint(i))
		}
	}
	work("a", "main")
	work("b", "a")
	work("c", "b")
	work("e", "a")    // a second stack on a
	work("d", "main") // alone
	r.Git("checkout", "-q", "main")
	r.Write("f", "1")
	r.Commit("trunk moves on")
	r.Git("branch", "old", "main~1") // where the stacks start, on the trunk

	var branches []Branch
	for _, line := range strings.Split(strings.TrimSpace(r.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")), "\n") {
		name, tip, _ := strings.Cut(line, " ")
		branches = append(branches, Branch{Name: name, Tip: tip})
	}
	trunk := []string{strings.TrimSpace(r.Git("rev-parse", "main"))}
	got, err := Find(context.Background(), testrun.Runner(t), r.GitDir(), branches, trunk)
	if err != nil {
		t.Fatal(err)
	}
	want := "[[refs/heads/a refs/heads/b refs/heads/c] [refs/heads/a refs/heads/e]]"
	if fmt.Sprint(got) != want {
		t.Fatalf("stacks %v, want %s", got, want)
	}
	if got, _ := Find(context.Background(), testrun.Runner(t), r.GitDir(), branches, nil); got != nil {
		t.Fatalf("stacks with no trunk: %v", got)
	}
}
