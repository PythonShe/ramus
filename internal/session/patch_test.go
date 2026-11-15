package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A file in Git LFS shows its content, read from the local LFS store, in
// place of the pointer git keeps, and says which sides were never
// downloaded.
func TestPatchSidesFromLFS(t *testing.T) {
	r := gittest.Init(t)
	pointer := func(content string) string {
		sum := sha256.Sum256([]byte(content))
		oid := hex.EncodeToString(sum[:])
		dir := filepath.Join(r.GitDir(), "lfs", "objects", oid[:2], oid[2:4])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, oid), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, len(content))
	}
	r.Write("art.psd", pointer("the old picture"))
	missing := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("ab", 32) + "\nsize 9\n"
	r.Write("gone.psd", missing)
	r.Commit("art")
	r.Write("art.psd", "the new picture")
	r.Write("gone.psd", "drawn again")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	read := func(path string) PatchResult {
		t.Helper()
		var got *PatchResult
		blob := r.Git("rev-parse", ":"+path)
		s.Patch(diff.PatchRequest{Paths: []string{path}, Context: -1}, Sides{Old: mustHex(t, blob[:40]), WorkTreePath: path},
			func(res PatchResult) { got = &res })
		m.until(t, "the patch", func() bool { return got != nil })
		if got.Err != nil {
			t.Fatal(got.Err)
		}
		return *got
	}
	res := read("art.psd")
	if !res.HasSides || !res.LFS || string(res.Old) != "the old picture" || string(res.New) != "the new picture" || res.NotFetched != [2]bool{} {
		t.Fatalf("sides %v lfs %v %q %q %v", res.HasSides, res.LFS, res.Old, res.New, res.NotFetched)
	}
	res = read("gone.psd")
	if !res.LFS || res.NotFetched != [2]bool{true, false} || string(res.Old) != missing {
		t.Fatalf("not downloaded: lfs %v %v %q", res.LFS, res.NotFetched, res.Old)
	}
}
