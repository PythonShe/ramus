package ui

import (
	"os"
	"path/filepath"
	"testing"

	"ramus/internal/git/write"
)

// A file's history follows its rename; its blame names the commit of each
// run of lines; both open from a path, as ramus log and blame do.
func TestFileHistoryAndBlame(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("src/a.go", "package a\n")
	r.Commit("add a")
	r.Git("mv", "src/a.go", "src/b.go")
	r.Commit("rename to b")
	r.Write("src/b.go", "package a\n\nfunc B() {}\n")
	r.Commit("add B")
	r.Write("src/b.go", "package a\n\nfunc B() {}\n// local\n")
	h.trust.Set(r.GitDir(), true)

	h.app.ShowFile(filepath.Join(r.Dir, "src", "b.go"), false)
	h.until("the history", func() bool { return hasExact(h, "add a") && hasExact(h, "rename to b") && hasExact(h, "add B") })
	if !hasExact(h, "as src/a.go") {
		t.Fatalf("the old name is not shown: %q", h.tt.Texts())
	}
	h.click("add a")
	h.until("the commit", h.shows("1 file changed"))

	h.click("Blame")
	h.until("the blame", func() bool {
		return hasExact(h, "func B() {}") && hasExact(h, "not committed") && !h.tt.HasText("Blaming")
	})
	tb := h.app.current()
	if len(tb.file.bcommits) != 3 {
		t.Fatalf("blamed on %d commits: %+v", len(tb.file.bcommits), tb.file.bcommits)
	}

	// Restore the file as it was before B; the change is left unstaged.
	h.app.ShowFile(filepath.Join(r.Dir, "src", "b.go"), false)
	h.until("the history again", func() bool { return hasExact(h, "rename to b") })
	h.menu("rename to b", "Restore File as of This Commit…")
	h.click("Restore")
	h.until("the restore", func() bool {
		b, _ := os.ReadFile(filepath.Join(r.Dir, "src", "b.go"))
		return string(b) == "package a\n"
	})
	if s := r.Git("status", "--porcelain"); s != " M src/b.go\n" {
		t.Fatalf("status after restore:\n%s", s)
	}

	// A folder's history, from the menu of the same tab.
	h.app.ShowFile(filepath.Join(r.Dir, "src"), false)
	h.until("the folder", func() bool { return tb.file.path == "src/" && hasExact(h, "add B") && !hasExact(h, "Blame") })
	if len(h.app.tabs) != 1 {
		t.Fatalf("%d tabs", len(h.app.tabs))
	}
}

// A write that leaves the file and HEAD as they were, as a fetch or
// another file staged, keeps the blame and the lines picked in it; one
// that changes the file blames it again.
func TestBlameOutlivesOtherWrites(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("one.txt", "a\nb\nc\n")
	r.Commit("one")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	fs := &tb.file
	r.Write("z.txt", "z\n")
	h.until("z listed", func() bool { return hasExact(h, "z.txt") })
	h.app.showFile(tb, "one.txt", true)
	h.until("the blame", func() bool { return fs.blameDone && fs.text != nil && !h.tt.HasText("Blaming") })
	text := &fs.text[0]
	fs.bpicked.Add(1)

	w := tb.s.Writes
	if _, err := tb.s.Submit(write.Stage(tb.s.Where(), []string{"z.txt"})); err != nil {
		t.Fatal(err)
	}
	h.until("the write counted", func() bool { return tb.s.Writes > w })
	h.idle(tb, "the stage")
	for range 10 {
		h.until("a frame", func() bool { return true })
	}
	if fs.text == nil || &fs.text[0] != text || !fs.bpicked.Has(1) {
		t.Fatalf("blamed again after an unrelated write; lines picked kept %v", fs.bpicked.Has(1))
	}

	r.Write("one.txt", "a\nb\nc\nd\n")
	w = tb.s.Writes
	if _, err := tb.s.Submit(write.Stage(tb.s.Where(), []string{"one.txt"})); err != nil {
		t.Fatal(err)
	}
	h.until("blamed again", func() bool {
		return tb.s.Writes > w && fs.blameDone && fs.text != nil && string(fs.text) == "a\nb\nc\nd\n"
	})

	// The view left, the blame is let go, and made again when shown.
	tb.sel = "changes"
	h.until("the changes", func() bool { return hasExact(h, "z.txt") })
	if fs.text != nil || fs.bcommits != nil {
		t.Fatal("the blame kept after the view was left")
	}
	h.app.showFile(tb, "one.txt", true)
	h.until("blamed when shown", func() bool { return fs.blameDone && string(fs.text) == "a\nb\nc\nd\n" })
}

// The history of lines chosen in the blame follows them back through a
// rename, one commit at a time with its patch of them.
func TestLineHistory(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("f.txt", "alpha\nbeta\ngamma\n")
	r.Commit("add f")
	r.Write("f.txt", "alpha\nBETA\ngamma\n")
	r.Commit("shout beta")
	r.Git("mv", "f.txt", "g.txt")
	r.Commit("rename to g")
	r.Write("g.txt", "alpha\nBETA\nGAMMA\n")
	r.Commit("shout gamma")
	h.trust.Set(r.GitDir(), true)

	h.app.ShowFile(filepath.Join(r.Dir, "g.txt"), true)
	h.until("the blame", func() bool { return hasExact(h, "BETA") && !h.tt.HasText("Blaming") })
	h.menu("BETA", "History of This Line")
	h.until("the line history", func() bool { return hasExact(h, "Line 2") && hasExact(h, "shout beta") && hasExact(h, "add f") })
	tb := h.app.current()
	if hasExact(h, "shout gamma") || len(tb.file.lines.log.Commits) != 2 {
		t.Fatalf("commits: %q", h.tt.Texts())
	}
	if !hasExact(h, "as f.txt") {
		t.Fatalf("the old name is not shown: %q", h.tt.Texts())
	}
	h.until("the patch of shout beta", patchHas(&tb.file.lines.pv, "BETA", "beta"))

	// Two lines chosen together, then back to the blame.
	h.click("Blame")
	h.until("the blame again", func() bool { return hasExact(h, "GAMMA") })
	tb.file.bpicked.Add(1)
	tb.file.bpicked.Add(2)
	h.menu("GAMMA", "History of These 2 Lines")
	h.until("both lines", func() bool {
		return hasExact(h, "Lines 2–3") && hasExact(h, "shout gamma") && hasExact(h, "shout beta")
	})
}

// A file asked for before git was found is shown once it is, and its
// repository open.
func TestShowFileBeforeGit(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("src/b.go")
	h.trust.Set(r.GitDir(), true)
	runner := h.app.Runner
	h.app.Runner = nil
	h.app.ShowFile(filepath.Join(r.Dir, "src", "b.go"), false)
	h.until("the repository asked for", func() bool { return len(h.app.pending) > 0 })
	h.app.SetRunner(runner, nil)
	tb := h.app.current()
	h.until("the file", func() bool { return tb.sel == "file" && tb.file.path == "src/b.go" })
}
