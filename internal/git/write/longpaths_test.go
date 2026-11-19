package write

import (
	"fmt"
	"strings"
	"testing"
)

// A write of fewer files than one group of any fixed count, whose names
// are long, and its undo, keep each command line within what Windows
// takes: what splits paths over commands counts their bytes.
func TestLongPathsKeepCommandLinesShort(t *testing.T) {
	h := newHarness(t)
	h.r.Write("other", "committed\n")
	h.r.Commit("one")
	var paths []string
	for i := range 200 {
		p := fmt.Sprintf("d/%s%03d", strings.Repeat("n", 170), i)
		h.r.Write(p, "untracked\n")
		paths = append(paths, p)
	}
	from := len(h.q.r.Journal().For(h.w.GitDir))
	op, err := Delete(h.w, paths)
	h.do(op, err, true)
	h.undo()
	if h.read(paths[0]) != "untracked\n" || h.read(paths[199]) != "untracked\n" {
		t.Fatal("the deleted files are not back")
	}
	// Folders still there once the write ran, which what reads them for
	// its undo asks git about.
	var dirs []string
	for i := range 200 {
		p := fmt.Sprintf("e/%s%03d", strings.Repeat("n", 170), i)
		h.r.Write(p+"/f", "committed\n")
		dirs = append(dirs, p)
	}
	h.r.Commit("folders")
	for _, d := range dirs {
		h.r.Write(d+"/f", "edited\n")
	}
	h.do(Discard(h.w, dirs), nil, true)
	h.undo()
	if h.read(dirs[0]+"/f") != "edited\n" {
		t.Fatal("the discarded edit is not back")
	}
	for _, rec := range h.q.r.Journal().For(h.w.GitDir)[from:] {
		if len(rec.Line) > 32767 {
			t.Errorf("a command line of %d bytes: %.120s", len(rec.Line), rec.Line)
		}
	}
}
