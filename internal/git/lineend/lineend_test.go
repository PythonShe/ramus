package lineend

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestCheck(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	r.Write("lf.txt", "a\nb\n")
	r.Write("crlf.txt", "a\r\nb\r\n")
	r.Write("attr.txt", "a\nb\n")
	r.Write(".gitattributes", "attr.txt text eol=lf\n")
	r.Commit("one")
	r.Write("lf.txt", "a\r\nb\r\n")        // CRLF brought into an LF file
	r.Write("crlf.txt", "a\r\nb\r\nc\r\n") // was CRLF already
	r.Write("mixed.txt", "a\nb\r\n")       // new, mixed
	r.Write("bin.dat", "\x00\r\n")         // binary
	r.Write("new.txt", "a\r\n")            // new, CRLF only
	r.Git("add", "lf.txt", "crlf.txt", "mixed.txt", "bin.dat", "new.txt")
	// Staged without filters, as another tool might have.
	r.Stage("attr.txt", "x\r\ny\n")
	var files []Staged
	for _, p := range []string{"lf.txt", "crlf.txt", "mixed.txt", "bin.dat", "new.txt", "attr.txt"} {
		s := Staged{Path: p, Index: oid(t, r, ":"+p)}
		if out, err := r.Run(nil, "rev-parse", "-q", "--verify", "HEAD:"+p); err == nil {
			s.Head, _ = hex.DecodeString(strings.TrimSpace(out))
		}
		files = append(files, s)
	}
	cat, err := catfile.Start(context.Background(), rn, r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	found, complete, err := Check(cat, files)
	if err != nil || !complete {
		t.Fatal(err, complete)
	}
	var got []string
	for _, f := range found {
		kind := "adds"
		if f.Mixed {
			kind = "mixed"
		}
		got = append(got, f.Path+":"+kind)
	}
	slices.Sort(got)
	if want := []string{"attr.txt:mixed", "lf.txt:adds", "mixed.txt:mixed"}; !slices.Equal(got, want) {
		t.Fatalf("found %q, want %q", got, want)
	}
	r.Git("config", "core.autocrlf", "input")
	autocrlf, _, err := Settings(context.Background(), rn, r.GitDir(), r.Dir, found)
	if err != nil || autocrlf != "input" {
		t.Fatal(err, autocrlf)
	}
	for _, f := range found {
		if f.Path == "attr.txt" && f.Attr != "text eol=lf" {
			t.Fatalf("attr %q", f.Attr)
		}
	}
}

func oid(t *testing.T, r *gittest.Repo, rev string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", rev)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
