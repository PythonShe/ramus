package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/git/diff"
	"ramus/internal/session"
)

// Lines picked in one file, then another file chosen: until the other
// file's patch is read, no line or change action is offered, and what
// would be applied names the file its lines are of.
func TestLinesStayWithTheirFile(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	body := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	r.Write("a.txt", body)
	r.Write("b.txt", body)
	r.Commit("ab")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("a.txt", strings.Replace(body, "12\n", "twelve\n", 1))
	r.Write("b.txt", strings.Replace(body, "1\n", "one\n", 1))
	h.until("the changes", func() bool { return h.tt.HasText("a.txt") && h.tt.HasText("b.txt") })
	tb := h.app.current()
	h.click("a.txt")
	d := &tb.cv.diff
	pv := &d.pv
	h.until("a's patch", patchHas(pv, "twelve", "12"))
	pv.d.SetSelectedLines(editor.DiffLineRange{Side: editor.DiffNew, Start: 11, End: 11}, true)
	h.until("the line picked", h.shows("Discard 1 Line…"))

	// No updates pumped: b's patch is not read yet.
	h.click("b.txt")
	h.tt.Frame()
	if d.res.Parsed == nil || d.res.Parsed.Path(0) != "a.txt" {
		t.Fatalf("a's patch no longer held while b's is read; the test shows nothing")
	}
	if h.tt.HasText("Discard 1 Line…") || h.tt.HasText("Stage 1 Line") || d.partialOK() {
		t.Fatalf("a's lines offered under b: partialOK %v, texts %q", d.partialOK(), h.tt.Texts())
	}
	if lt := tb.shownLines(); lt.p != nil && lt.p.Path(0) != lt.path {
		t.Fatalf("the lines of %s would be applied as %s", lt.p.Path(0), lt.path)
	}
	if _, n := pv.selectedLines(); n != 0 {
		t.Fatalf("%d lines of a.txt still picked with b.txt chosen", n)
	}

	h.until("b's patch", patchHas(pv, "one", "1"))
	if !d.partialOK() {
		t.Fatal("b's own patch offers no line actions")
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(r.Dir, name)); return string(b) }
	if !strings.Contains(read("a.txt"), "twelve") {
		t.Fatal("a.txt changed")
	}
}

// A file in Git LFS shows the content its pointers stand for, but git
// applies lines to the pointers: none can be picked.
func TestNoLinesOfAnLFSPointer(t *testing.T) {
	text := "diff --git a/a.dat b/a.dat\nindex 1111111..2222222 100644\n--- a/a.dat\n+++ b/a.dat\n" +
		"@@ -1,3 +1,3 @@\n version https://git-lfs.github.com/spec/v1\n" +
		"-oid sha256:" + strings.Repeat("a", 64) + "\n-size 4\n" +
		"+oid sha256:" + strings.Repeat("b", 64) + "\n+size 11\n"
	p, err := diff.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	key := diffKey{path: "a.dat", src: diff.Unstaged, ws: diff.AllWhitespace}
	if !partialOK(session.PatchResult{Parsed: p}, key) {
		t.Fatal("a text patch offers no lines; the test shows nothing")
	}
	if partialOK(session.PatchResult{Parsed: p, LFS: true, HasSides: true}, key) {
		t.Fatal("lines of an LFS pointer offered")
	}
}
