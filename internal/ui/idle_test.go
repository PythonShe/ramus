package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// With repositories open and nothing changing, the app does nothing: in
// every section it asks for no frame, posts nothing to the main thread
// and runs no git, so it costs no CPU (GHD #1641). A read that touched
// what the watcher watches would show here as a loop.
func TestIdle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as git")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	bin := t.TempDir()
	logFile := filepath.Join(bin, "log")
	script := "#!/bin/sh\necho \"$*\" >> " + logFile + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitexe.Find(context.Background(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	ran := func() int {
		data, _ := os.ReadFile(logFile)
		return strings.Count(string(data), "\n")
	}
	h := newHarnessWith(t, run.New(g, gittest.Env(), 8))
	other := h.repoWith()
	r := h.repoWith()
	for _, f := range []string{"a", "b"} {
		r.Write(f+".txt", f+"\n")
		r.Commit("add " + f)
	}
	r.Git("tag", "v1")
	r.Write("a.txt", "a, changed\n")
	h.trust.Set(r.GitDir(), true)
	h.trust.Set(other.GitDir(), true)
	h.app.Open(other.Dir)
	h.until("the other repository", h.shows("No changes"))
	h.app.Open(r.Dir)
	r.Write("untracked.txt", "x\n")
	h.until("the repository", h.shows("untracked.txt"))
	if !strings.Contains(func() string { b, _ := os.ReadFile(logFile); return string(b) }(), "status") {
		t.Fatal("the app's git runs are not logged")
	}

	quiet := func(what string) {
		t.Helper()
		// Let what the view started finish: a spinner asks for frames
		// until its work ends.
		h.until(what+" settled", func() bool {
			select {
			case fn := <-h.updates:
				fn()
				return false
			case <-time.After(300 * time.Millisecond):
			}
			h.tt.Frame()
			return !h.tt.FramePending()
		})
		before := ran()
		posted := 0
		end := time.After(1200 * time.Millisecond)
		for wait := true; wait; {
			select {
			case fn := <-h.updates:
				fn()
				posted++
			case <-end:
				wait = false
			}
		}
		h.tt.Frame()
		if posted > 0 || ran() != before || h.tt.FramePending() {
			data, _ := os.ReadFile(logFile)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			t.Fatalf("%s is not idle: %d updates, %d git runs, frame pending %t; last runs %q",
				what, posted, ran()-before, h.tt.FramePending(), lines[max(0, len(lines)-5):])
		}
	}
	for _, section := range []string{"Changes (2)", "History", "Reflog", "Branches and Tags", "Statistics", "Command Log", "Snapshots"} {
		h.click(section)
		quiet(section)
	}
	h.app.ShowFile(filepath.Join(r.Dir, "a.txt"), true)
	h.until("the blame", func() bool { return hasExact(h, "a, changed") && !h.tt.HasText("Blaming") })
	quiet("the blame")
}
