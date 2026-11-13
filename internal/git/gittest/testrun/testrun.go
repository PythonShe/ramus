// Package testrun gives tests a runner for the git on PATH, isolated from
// the machine's configuration as gittest's commands are. It is apart from
// gittest so that the runner's own tests can use gittest.
package testrun

import (
	"context"
	"os"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// Runner returns a runner of up to 8 processes, or skips the test when
// there is no git.
func Runner(t testing.TB) *run.Runner {
	t.Helper()
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	return run.New(g, gittest.Env(), 8)
}
