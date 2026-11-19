//go:build !windows

package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// The read of a conflicted file stops, its processes with it, when the
// merge editor leaves the file for another conflicted file, for a file
// that is not conflicted, or for another view: the file left is no
// longer waited on, and the one chosen is read at once.
func TestMergeLeftStopsTheRead(t *testing.T) {
	real := testrun.Git(t)
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	// git, but the read of slow.txt's stages never ends: it notes its
	// process and waits to be stopped.
	wrapper := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = slow.txt ]; then case \" $* \" in *\" ls-files \"*) echo $$ >>'" + started + "'; exec sleep 600;; esac; fi\n" +
		"done\n" +
		"exec '" + real.Path + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, run.New(&gitexe.Git{Path: wrapper, Version: real.Version}, gittest.Env(), 8))
	r := h.repoWith()
	for _, f := range []string{"slow.txt", "a.txt", "b.txt"} {
		r.Write(f, "base "+f+"\n")
	}
	r.Commit("base")
	r.Git("switch", "-q", "-c", "topic")
	r.Write("slow.txt", "theirs\n")
	r.Write("a.txt", "theirs\n")
	r.Write("b.txt", "merged cleanly\n")
	r.Commit("topic")
	r.Git("switch", "-q", "main")
	r.Write("slow.txt", "ours\n")
	r.Write("a.txt", "ours\n")
	r.Commit("main")
	r.Run(nil, "merge", "topic")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the conflicts", func() bool { return hasExact(h, "slow.txt") && hasExact(h, "a.txt") && hasExact(h, "b.txt") })

	// pids are the reads of slow.txt started so far.
	pids := func() []int {
		b, _ := os.ReadFile(started)
		var out []int
		for _, f := range strings.Fields(string(b)) {
			n, err := strconv.Atoi(f)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	// running counts the reads of slow.txt still running.
	running := func() int {
		n := 0
		for _, pid := range pids() {
			if syscall.Kill(pid, 0) == nil {
				n++
			}
		}
		return n
	}
	read := func(k int) {
		t.Helper()
		h.click("slow.txt")
		h.until("the read of slow.txt", func() bool { return len(pids()) == k })
	}

	read(1)
	h.click("a.txt")
	h.until("a.txt shown, the read of slow.txt stopped", func() bool {
		return h.tt.HasText("Conflict 1") && tb.merge.path == "a.txt" && running() == 0
	})

	read(2)
	h.click("b.txt")
	h.until("the read stopped for a file not conflicted", func() bool { return running() == 0 })

	read(3)
	h.click("History")
	h.until("the read stopped with the view", func() bool { return running() == 0 })
}
