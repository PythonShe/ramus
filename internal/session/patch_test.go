package session

import (
	"testing"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// A patch comes with the whole files it was made from, and a patch that
// leaves white space out shows fewer changes.
func TestPatchSides(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\ntwo\nthree\n")
	r.Commit("a")
	r.Write("a.txt", "one\n  two\nTHREE\n")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	index := r.Git("rev-parse", ":a.txt")
	read := func(req diff.PatchRequest, sides Sides) PatchResult {
		t.Helper()
		var got *PatchResult
		s.Patch(req, sides, func(res PatchResult) { got = &res })
		m.until(t, "the patch", func() bool { return got != nil })
		if got.Err != nil {
			t.Fatal(got.Err)
		}
		return *got
	}
	res := read(diff.PatchRequest{Paths: []string{"a.txt"}, Context: -1},
		Sides{Old: mustHex(t, index[:40]), WorkTreePath: "a.txt"})
	if !res.HasSides || string(res.Old) != "one\ntwo\nthree\n" || string(res.New) != "one\n  two\nTHREE\n" {
		t.Fatalf("sides %v %q %q", res.HasSides, res.Old, res.New)
	}
	changed := func(p *diff.Parsed) int {
		n := 0
		for _, l := range p.Lines {
			if l.Kind == '+' {
				n++
			}
		}
		return n
	}
	if n := changed(res.Parsed); n != 2 {
		t.Fatalf("%d added lines", n)
	}
	ws := read(diff.PatchRequest{Paths: []string{"a.txt"}, Context: -1, Whitespace: diff.IgnoreAllSpace}, Sides{})
	if n := changed(ws.Parsed); n != 1 || ws.HasSides {
		t.Fatalf("ignoring white space: %d added lines, sides %v", n, ws.HasSides)
	}
}
