package history

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// A row's text holds its subject and author, and for a merge the parents
// it merges, as git log's %p abbreviates them to seven digits.
func TestLoadTextMerged(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1\n")
	r.Commit("one")
	r.Git("checkout", "-q", "-b", "side")
	r.Write("b", "2\n")
	r.Commit("two")
	r.Git("checkout", "-q", "main")
	r.Write("c", "3\n")
	r.Commit("three")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	var ids [][]byte
	var want []string
	for _, line := range strings.Split(strings.TrimSpace(r.Git("log", "--format=%H %P")), "\n") {
		f := strings.Fields(line)
		id, _ := hex.DecodeString(f[0])
		ids = append(ids, id)
		var short []string
		for _, p := range f[1:] {
			short = append(short, p[:7])
		}
		if len(short) < 2 {
			short = nil
		}
		want = append(want, strings.Join(short, " and "))
	}
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := LoadText(p, func(id uint32) []byte { return ids[id] }, 0, len(ids), func(row int) uint32 { return uint32(row) })
	if err != nil {
		t.Fatal(err)
	}
	for i := range ids {
		subject, _, _ := c.Row(i)
		if got := c.Merged(i); got != want[i] {
			t.Errorf("row %d (%s): merged %q, want %q", i, subject, got, want[i])
		}
	}
	if s, a, _ := c.Row(0); s != "merge side" || a == "" {
		t.Fatalf("row 0: %q by %q", s, a)
	}
}

// A row's text is UTF-8: converted from the encoding its commit names, or
// with the bytes that are not UTF-8 escaped and the row marked.
func TestLoadTextEncodings(t *testing.T) {
	r := gittest.Init(t)
	commit := func(msg string, enc string) {
		t.Helper()
		r.Write("f", msg)
		r.Git("add", "f")
		args := []string{"commit", "-q", "-F", "-"}
		if enc != "" {
			args = append([]string{"-c", "i18n.commitEncoding=" + enc}, args...)
		}
		if _, err := r.Run([]byte(msg), args...); err != nil {
			t.Fatal(err)
		}
	}
	commit("caf\xe9 latin", "ISO-8859-1")
	// git commit turns bytes that are not UTF-8 into it as Latin-1;
	// older tools did not, so such a commit is written by hand.
	tree := strings.TrimSpace(r.Git("rev-parse", "HEAD^{tree}"))
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	raw := "tree " + tree + "\nparent " + head + "\nauthor A <a@x> 1700000000 +0000\ncommitter A <a@x> 1700000000 +0000\n\ncaf\xe9 raw\n"
	id, err := r.Run([]byte(raw), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	r.Git("update-ref", "HEAD", strings.TrimSpace(id))
	commit("plain", "")
	var ids [][]byte
	for _, line := range strings.Fields(r.Git("log", "--format=%H")) {
		id, _ := hex.DecodeString(line)
		ids = append(ids, id)
	}
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := LoadText(p, func(id uint32) []byte { return ids[id] }, 0, len(ids), func(row int) uint32 { return uint32(row) })
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		subject string
		odd     bool
	}{{"plain", false}, {`caf\xe9 raw`, true}, {"café latin", false}} {
		if s, _, _ := c.Row(i); s != want.subject || c.Odd(i) != want.odd {
			t.Errorf("row %d: %q odd %v; want %q odd %v", i, s, c.Odd(i), want.subject, want.odd)
		}
	}
}
