package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

func conflictingRepo(h *harness) *gittest.Repo {
	r := h.repoWith()
	r.Write("a.txt", "one\n")
	r.Write("b.bin", "\x00base")
	r.Write("c.txt", "c\n")
	r.Commit("base")
	r.Git("switch", "-q", "-c", "topic")
	r.Write("a.txt", "theirs\n")
	r.Write("b.bin", "\x00theirs")
	r.Git("rm", "-q", "c.txt")
	r.Commit("topic")
	r.Git("switch", "-q", "main")
	r.Write("a.txt", "ours\n")
	r.Write("b.bin", "\x00ours")
	r.Write("c.txt", "c changed\n")
	r.Commit("main")
	return r
}

// A merge previewed, then stopped on conflicts that are resolved in the
// app one way each, then continued.
func TestMergeAndResolve(t *testing.T) {
	h := newHarness(t)
	r := conflictingRepo(h)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(r.Dir, name)); return string(b) }

	h.click("Branches and Tags")
	h.until("the branches", func() bool { return tb.refs.info != nil })
	h.tt.Frame()
	h.menu("topic", "Merge into main…")
	h.until("the preview", h.shows("3 files will conflict"))
	if !hasExact(h, "a.txt") || !hasExact(h, "conflict") {
		t.Fatalf("preview %q", h.tt.Texts())
	}
	h.click("Merge")
	h.until("the stop", h.shows("Merge stopped: 3 conflicted files left"))

	tb.sel = "changes"
	h.tt.Frame()
	h.until("the conflicts", func() bool { return hasExact(h, "a.txt") })
	h.click("a.txt")
	h.until("the editor", h.shows("Conflict 1"))
	h.click("Theirs")
	h.click("Save and Mark Resolved")
	h.until("a.txt resolved", func() bool { return read("a.txt") == "theirs\n" && !strings.Contains(r.Git("ls-files", "-u"), "a.txt") })
	h.idle(tb, "the resolve")

	h.until("b.bin listed", func() bool { return hasExact(h, "b.bin") })
	h.click("b.bin")
	h.until("the sides", h.shows("Binary file changed on both sides"))
	h.click("Take Ours")
	h.until("b.bin resolved", func() bool { return !strings.Contains(r.Git("ls-files", "-u"), "b.bin") })
	h.idle(tb, "the take")

	h.until("c.txt listed", func() bool { return hasExact(h, "c.txt") })
	h.menu("c.txt", "Take Theirs for File")
	h.until("c.txt resolved", func() bool { return r.Git("ls-files", "-u") == "" })
	if _, err := os.Stat(filepath.Join(r.Dir, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("c.txt kept though theirs deleted it")
	}
	h.idle(tb, "the delete")

	h.until("no conflicts left", func() bool {
		st := tb.s.State.Status
		return st != nil && st.Unmerged() == 0 && h.tt.HasText("Merge stopped")
	})
	h.click("Continue")
	h.idle(tb, "the continue")
	if l := tb.s.Log; l[len(l)-1].Err != nil || h.app.notice != "" {
		t.Fatalf("%s: %v; notice %q; output %q", l[len(l)-1].Name, l[len(l)-1].Err, h.app.notice, l[len(l)-1].Output)
	}
	h.until("the merge commit", func() bool { return len(strings.Fields(r.Git("log", "-1", "--format=%P"))) == 2 })
	h.until("the bar gone", func() bool { return !h.tt.HasText("Merge stopped") })
}

func TestAbortMerge(t *testing.T) {
	h := newHarness(t)
	r := conflictingRepo(h)
	main := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Run(nil, "merge", "topic")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the bar", h.shows("Merge stopped"))
	h.click("Abort…")
	h.until("the question", h.shows("Abort the merge?"))
	h.click("Abort")
	h.until("aborted", func() bool {
		_, err := os.Stat(filepath.Join(r.GitDir(), "MERGE_HEAD"))
		return os.IsNotExist(err) && strings.TrimSpace(r.Git("status", "--porcelain")) == ""
	})
	if strings.TrimSpace(r.Git("rev-parse", "HEAD")) != main {
		t.Fatal("HEAD moved")
	}
}
