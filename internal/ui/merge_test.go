package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ramus/internal/git/conflict"
	"ramus/internal/git/gittest"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
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
	h.until("the repository", h.shows("No local changes"))
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
	h.click("Fast-Forward If Possible")
	h.click("Create Merge Commit")
	if m := mergeModes[h.app.mergeUI.mode]; m != write.MergeNoFF {
		t.Fatalf("mode %v chosen", m)
	}
	h.click("Merge")
	h.until("the stop", h.shows("Merge stopped: 3 conflicted files left"))

	tb.sel = "changes"
	h.tt.Frame()
	h.until("the conflicts", func() bool { return hasExact(h, "a.txt") })
	// Each conflict says what kind it is.
	if !hasExact(h, "both modified") || !hasExact(h, "deleted by them") {
		t.Fatalf("kinds of conflict %q", h.tt.Texts())
	}
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

// A long run of unchanged text opened keeps its last lines, those next
// to the conflict after it, and counts what it leaves out.
func TestOpenedText(t *testing.T) {
	var b strings.Builder
	for i := range maxOpenedLines + 500 {
		b.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	got := openedText([]byte(b.String()))
	last := "line " + strconv.Itoa(maxOpenedLines+499)
	if !strings.HasPrefix(got, "line 0\n") || !strings.HasSuffix(got, last) || !strings.Contains(got, "\n⋯ 500 more lines\n") {
		t.Fatalf("opened %.20q…%q", got, got[max(0, len(got)-40):])
	}
	if got := openedText([]byte("a\nb\n")); got != "a\nb" {
		t.Fatalf("short run %q", got)
	}
}

// A write that leaves the conflicted file as it was, as a background
// fetch or another file staged, keeps the choices made and the text
// typed; one that changed the file on disk starts again, and says so.
func TestMergeChoicesOutliveOtherWrites(t *testing.T) {
	h := newHarness(t)
	r := conflictingRepo(h)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	r.Run(nil, "merge", "topic")
	h.until("the conflicts", func() bool { return hasExact(h, "a.txt") })
	r.Write("z.txt", "z\n")
	r.Write("y.txt", "y\n")
	h.until("z listed", func() bool { return hasExact(h, "z.txt") && hasExact(h, "y.txt") })
	h.click("a.txt")
	h.until("the editor", h.shows("Conflict 1"))
	ms := &tb.merge
	h.click("Edit")
	ms.custom[0] = "typed by hand\n"

	stage := func(path string) {
		t.Helper()
		w := tb.s.Writes
		if _, err := tb.s.Submit(write.Stage(tb.s.Where(), []string{path})); err != nil {
			t.Fatal(err)
		}
		h.until("the write counted", func() bool { return tb.s.Writes > w })
		h.until("reloaded", func() bool { return ms.readW == tb.s.Writes && !ms.loading })
	}
	stage("z.txt")
	if ms.choices[0] != conflict.Custom || ms.custom[0] != "typed by hand\n" {
		t.Fatalf("after an unrelated write: choice %v, text %q", ms.choices[0], ms.custom[0])
	}
	if ms.err != "" {
		t.Fatalf("an unchanged file said %q", ms.err)
	}

	r.Write("a.txt", "<<<<<<< HEAD\nours again, longer\n=======\ntheirs again\n>>>>>>> topic\n")
	stage("y.txt")
	if ms.choices[0] != conflict.Unresolved {
		t.Fatalf("a file changed on disk kept choice %v", ms.choices[0])
	}
	h.until("why", h.shows(locales.MergeChangedOnDisk()))
}

// A conflict that failed to load says why, and offers no side to take:
// both would have read as deleted.
func TestMergeLoadErrorIsNotADeletion(t *testing.T) {
	h := newHarness(t)
	r := conflictingRepo(h)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	r.Run(nil, "merge", "topic")
	h.until("the conflicts", func() bool { return hasExact(h, "a.txt") })
	h.click("a.txt")
	h.until("the editor", h.shows("Conflict 1"))
	tb.merge.cf = session.ConflictFile{Path: "a.txt", Err: errors.New("git ls-files failed")}
	h.tt.Frame()
	if !h.tt.HasText("git ls-files failed") || h.tt.HasText(locales.MergeDeletedHere()) || h.tt.HasText(locales.MergeDeleteIt()) {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	// ls-files found no stage: resolved outside the app, before the
	// status read says so.
	tb.merge.cf = session.ConflictFile{Path: "a.txt"}
	h.tt.Frame()
	if h.tt.HasText(locales.MergeDeletedHere()) || h.tt.HasText(locales.MergeDeleteIt()) {
		t.Fatalf("no stages: texts %q", h.tt.Texts())
	}
	// Deleted on both sides: git keeps the base stage alone.
	tb.merge.cf = session.ConflictFile{Path: "a.txt", Staged: true}
	h.tt.Frame()
	if !h.tt.HasText(locales.MergeDeleteIt()) {
		t.Fatalf("both deleted: texts %q", h.tt.Texts())
	}
}
