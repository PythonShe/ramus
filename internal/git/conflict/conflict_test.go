package conflict

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// git merge-file is the oracle: what it writes parses into the sides it
// merged, and resolving every conflict one way gives what git writes when
// told to favour that side.
func TestAgainstMergeFile(t *testing.T) {
	r := gittest.Init(t)
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	base := "a\nb\nc\nd\ne\nf\ng\nh\n"
	ours := "a\nB ours\nc\nd\ne\nf\nG ours\nh\n"
	theirs := "a\nB theirs\nb2\nc\nd\ne\nf\nG theirs\nh\n"
	for _, style := range []string{"merge", "diff3", "zdiff3"} {
		o, b, th := write("o", ours), write("b", base), write("t", theirs)
		args := []string{"merge-file", "-p", "-L", "ours", "-L", "base", "-L", "theirs"}
		if style != "merge" {
			args = append(args, "--"+style)
		}
		out, _ := r.Run(nil, append(args, o, b, th)...)
		segs, err := Parse([]byte(out))
		if err != nil {
			t.Fatalf("%s: %v\n%s", style, err, out)
		}
		if n := Count(segs); n != 2 {
			t.Fatalf("%s: %d conflicts\n%s", style, n, out)
		}
		c := segs[1].Conflict
		if c == nil || c.OursLabel != "ours" || c.TheirsLabel != "theirs" || c.HasBase != (style != "merge") {
			t.Fatalf("%s: %+v", style, c)
		}
		for _, side := range []struct {
			flag   string
			choice Choice
		}{{"--ours", Ours}, {"--theirs", Theirs}, {"--union", OursThenTheirs}} {
			want, _ := r.Run(nil, "merge-file", "-p", side.flag, o, b, th)
			got, err := Resolve(segs, []Choice{side.choice, side.choice}, nil)
			if err != nil || string(got) != want {
				t.Errorf("%s %s:\n%s\nwant\n%s", style, side.flag, got, want)
			}
		}
		if _, err := Resolve(segs, []Choice{Ours}, nil); err == nil {
			t.Errorf("%s: resolved with a conflict left", style)
		}
	}
}

func TestParseEdges(t *testing.T) {
	// A heading underline is text, as is a marker inside a side.
	doc := "Title\n=======\n<<<<<<< HEAD\nx\n>>>>>>>> not a close\n=======\ny\n>>>>>>> topic\n"
	segs, err := Parse([]byte(doc))
	if err != nil || Count(segs) != 1 {
		t.Fatalf("%v, %d", err, Count(segs))
	}
	if string(segs[0].Text) != "Title\n=======\n" || string(segs[1].Conflict.Ours) != "x\n>>>>>>>> not a close\n" {
		t.Fatalf("%q %q", segs[0].Text, segs[1].Conflict.Ours)
	}
	if _, err := Parse([]byte("<<<<<<< HEAD\nx\n=======\n")); err != ErrMarkers {
		t.Fatalf("unclosed: %v", err)
	}
	// CRLF files keep their endings, and both sides join on one.
	segs, _ = Parse([]byte("<<<<<<< a\r\nx\r\n=======\r\ny\r\n>>>>>>> b\r\n"))
	got, _ := Resolve(segs, []Choice{TheirsThenOurs}, nil)
	if string(got) != "y\r\nx\r\n" {
		t.Fatalf("%q", got)
	}
	segs, _ = Parse([]byte("<<<<<<< a\nx\n=======\ny\n>>>>>>> b\n"))
	got, _ = Resolve(segs, []Choice{Custom}, [][]byte{[]byte("z\n")})
	if string(got) != "z\n" {
		t.Fatalf("%q", got)
	}
	if _, err := Parse(bytes.Repeat([]byte("x"), MaxSize+1)); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatal("no size bound")
	}
}
