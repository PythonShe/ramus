package ui

import (
	"path/filepath"
	"testing"
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

	// A folder's history, from the menu of the same tab.
	h.app.ShowFile(filepath.Join(r.Dir, "src"), false)
	h.until("the folder", func() bool { return tb.file.path == "src/" && hasExact(h, "add B") && !hasExact(h, "Blame") })
	if len(h.app.tabs) != 1 {
		t.Fatalf("%d tabs", len(h.app.tabs))
	}
}
