package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// Leaving a view ends the git processes it started, and their answers
// never come.
func TestLeavingAViewCancelsItsReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as git")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	// A git whose file history never ends.
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = --name-only ] && exec sleep 60; done\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitexe.Find(context.Background(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	runner := run.New(g, gittest.Env(), 8)

	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil })
	m.pump(4 * Debounce)

	s.SetView("file")
	answered := false
	s.FileLog("a", FileLogOptions{}, func([]FileCommit, bool, error) { answered = true })
	deadline := time.Now().Add(10 * time.Second)
	for runner.Running() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the file history never started")
		}
		m.pump(10 * time.Millisecond)
	}
	s.SetView("changes")
	for runner.Running() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d git processes still running after the view was left", runner.Running())
		}
		m.pump(10 * time.Millisecond)
	}
	m.pump(200 * time.Millisecond)
	if answered {
		t.Fatal("a read of the view left answered")
	}
}
