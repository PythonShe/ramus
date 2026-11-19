package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// wrappedGit returns a runner whose git is a shell script that runs body,
// which sees git's arguments as "$@", and then the real git; FLAG in
// body is replaced by flag.
func wrappedGit(t *testing.T, body, flag string) *run.Runner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as git")
	}
	real := gittest.GitPath(t)
	bin := t.TempDir()
	script := "#!/bin/sh\n" + strings.ReplaceAll(body, "FLAG", flag) + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitexe.Find(context.Background(), bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	return run.New(g, gittest.Env(), 8)
}

// A line history left while it reads is read again when shown again,
// not left loading for good.
func TestLeftLineHistoryReadsAgain(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "hang")
	h := newHarnessWith(t, wrappedGit(t, `if [ -e FLAG ]; then for a in "$@"; do case "$a" in -L*) exec sleep 61;; esac; done; fi`, flag))
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.ShowFile(filepath.Join(r.Dir, "README"), true)
	h.until("the blame", func() bool { return hasExact(h, "hello") && !h.tt.HasText("Blaming") })
	tb := h.app.current()
	os.WriteFile(flag, nil, 0o644)
	h.menu("hello", "History of This Line")
	h.until("the line history read", func() bool { return tb.file.lines.loading })
	h.click("Changes")
	h.until("the view left", func() bool { return tb.shown != "file" })
	os.Remove(flag)
	tb.sel = "file"
	h.until("the line history read again", func() bool { return hasExact(h, "Line 1") && hasExact(h, "one") })
}

// A line history is read again when HEAD moves, not on every change of
// the refs, as a background fetch makes.
func TestLineHistoryKeepsThroughRefChanges(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	h := newHarnessWith(t, wrappedGit(t, `for a in "$@"; do case "$a" in -L*) echo x >> FLAG;; esac; done`, count))
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.ShowFile(filepath.Join(r.Dir, "README"), true)
	h.until("the blame", func() bool { return hasExact(h, "hello") && !h.tt.HasText("Blaming") })
	tb := h.app.current()
	h.until("HEAD read", func() bool { return len(tb.s.State.Head.OID) > 0 })
	h.menu("hello", "History of This Line")
	h.until("the line history", func() bool { return hasExact(h, "one") && !tb.file.lines.loading })
	runs := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	if runs() != 1 {
		t.Fatalf("%d runs of git log -L", runs())
	}
	refs := tb.s.State.Refs
	r.Git("tag", "t1")
	h.until("the new tag", func() bool { return tb.s.State.Refs != refs })
	h.pumpFor(200 * time.Millisecond)
	if runs() != 1 {
		t.Fatalf("a new tag ran git log -L again: %d runs", runs())
	}
	r.Write("other", "x")
	r.Commit("two")
	h.until("the history read again once HEAD moved", func() bool { return runs() == 2 && !tb.file.lines.loading })
}

// The history of a file asked for while another's was read is never
// shown with the other file's commits.
func TestFileHistoryOfTheFileShown(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "slow")
	h := newHarnessWith(t, wrappedGit(t, `if [ -e FLAG ]; then for a in "$@"; do [ "$a" = one.txt ] && sleep 2; done; fi`, flag))
	r := h.repoWith()
	r.Write("one.txt", "a\n")
	r.Commit("only one")
	r.Write("two.txt", "b\n")
	r.Commit("only two")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	os.WriteFile(flag, nil, 0o644)
	h.app.ShowFile(filepath.Join(r.Dir, "one.txt"), false)
	h.until("one.txt's history read", func() bool { return tb.file.logLoading })
	h.app.ShowFile(filepath.Join(r.Dir, "two.txt"), false)
	start := time.Now()
	h.until("one.txt's history read to its end", func() bool {
		if tb.file.path == "two.txt" && hasExact(h, "only one") {
			t.Fatalf("two.txt's history lists one.txt's commit: %q", h.tt.Texts())
		}
		return time.Since(start) > 3*time.Second && hasExact(h, "only two")
	})
}
