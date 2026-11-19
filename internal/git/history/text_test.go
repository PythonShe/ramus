package history

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// A read its caller gave up on stops, with the caller's error.
func TestLoadTextIsCancelled(t *testing.T) {
	r := gittest.Init(t)
	id, _ := hex.DecodeString(r.Commit("one"))
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadText(ctx, p, [][]byte{id}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

// A subject of one long paragraph is kept to what a row could show, cut
// on a character.
func TestLoadTextClipsTheSubject(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1\n")
	r.Commit("one")
	long := "x" + strings.Repeat("é", 1<<19)
	out, err := r.Run([]byte(long), "commit-tree", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := hex.DecodeString(strings.TrimSpace(out))
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := LoadText(context.Background(), p, [][]byte{id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	subject, _, _ := c.Row(0)
	if len(subject) > MaxSubject || len(subject) < MaxSubject-4 || !strings.HasPrefix(long, subject) || !utf8.ValidString(subject) {
		t.Fatalf("kept %d bytes of the subject", len(subject))
	}
	if c.Bytes() > 2*MaxSubject+1024 {
		t.Fatalf("the chunk holds %d bytes", c.Bytes())
	}
}

// A parent named after the author line is no parent, as git reads it,
// and the merged text does not name it.
func TestLoadTextMergedParentsAsGitReadsThem(t *testing.T) {
	r := gittest.Init(t)
	a := r.Commit("a")
	b := r.Commit("b")
	c := r.Commit("c")
	tree := strings.TrimSpace(r.Git("rev-parse", "HEAD^{tree}"))
	body := "tree " + tree + "\nparent " + a + "\nparent " + b + "\nauthor A <a@x> 1 +0000\nparent " + c + "\ncommitter A <a@x> 1 +0000\n\nm\n"
	out, err := r.Run([]byte(body), "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := hex.DecodeString(strings.TrimSpace(out))
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	chunk, err := LoadText(context.Background(), p, [][]byte{id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := chunk.Merged(0), a[:7]+" and "+b[:7]; got != want {
		t.Fatalf("merged %q, want %q", got, want)
	}
}

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
	var emails []string
	for _, line := range strings.Split(strings.TrimSpace(r.Git("log", "--format=%H %P%x00%aE")), "\n") {
		line, email, _ := strings.Cut(line, "\x00")
		emails = append(emails, email)
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
	c, err := LoadText(context.Background(), p, ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ids {
		subject, _, _ := c.Row(i)
		if !bytes.Equal(c.OID(i), ids[i]) {
			t.Errorf("row %d is %x, want %x", i, c.OID(i), ids[i])
		}
		if got := c.Email(i); got != emails[i] {
			t.Errorf("row %d (%s): email %q, want %q", i, subject, got, emails[i])
		}
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
	// A subject and an author name that disguise themselves with a bidi
	// override and a terminal escape are shown without them.
	r.Write("f", "bidi")
	r.Git("add", "f")
	r.Git("commit", "-q", "--author", "Ev\u202eil\x1b[2J <e@x>", "-m", "fix \u202egnp.exe\u200b")
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
	c, err := LoadText(context.Background(), p, ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		subject string
		odd     bool
	}{{"fix gnp.exe", false}, {"plain", false}, {`caf\xe9 raw`, true}, {"café latin", false}} {
		if s, au, _ := c.Row(i); s != want.subject || c.Odd(i) != want.odd || i == 0 && au != "Evil" {
			t.Errorf("row %d: %q by %q odd %v; want %q odd %v", i, s, au, c.Odd(i), want.subject, want.odd)
		}
	}
}
