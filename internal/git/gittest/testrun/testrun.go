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

// Runner returns a runner of up to 8 processes of the git Git finds.
func Runner(t testing.TB) *run.Runner {
	t.Helper()
	return run.New(Git(t), gittest.Env(), 8)
}

// Git finds the git on PATH as the app does, and fails the test when
// there is none, as gittest.GitPath does, rather than skip it. Finding
// asks git its version within a bound a loaded machine may overrun, so
// it is asked a few times before the test fails.
func Git(t testing.TB) *gitexe.Git {
	t.Helper()
	gittest.GitPath(t)
	var err error
	for range 3 {
		var g *gitexe.Git
		if g, err = gitexe.Find(context.Background(), os.Getenv("PATH")); err == nil {
			return g
		}
	}
	t.Fatalf("git cannot be found: %v", err)
	return nil
}
