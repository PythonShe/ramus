package pathtree

import (
	"strings"
	"testing"
)

func render(t *Tree, n Node, depth int, b *strings.Builder) {
	for _, k := range t.Children(n) {
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString(t.Name(k))
		if t.IsDir(k) {
			b.WriteString("/\n")
			render(t, k, depth+1, b)
		} else {
			b.WriteString("\n")
		}
	}
}

func TestBuild(t *testing.T) {
	paths := []string{"a.txt", "a/b/c.go", "a/b/d.go", "a/e", "a/f/g", "ab", "z/y/x/w", "z/y/x/w", "日本/語"}
	tr := Build([]byte(strings.Join(paths, "\x00")+"\x00"), 0)
	var b strings.Builder
	render(tr, Root, 0, &b)
	want := `a/
  b/
    c.go
    d.go
  f/
    g
  e
z/
  y/
    x/
      w
日本/
  語
a.txt
ab
`
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
	if tr.Files() != 8 {
		t.Fatalf("files %d", tr.Files())
	}
	// Index points back into the input, past the duplicate.
	for n := Node(0); int(n) < tr.Len(); n++ {
		if !tr.IsDir(n) && !strings.HasSuffix(paths[tr.Index(n)], tr.Name(n)) {
			t.Fatalf("%s indexes %q", tr.Name(n), paths[tr.Index(n)])
		}
	}
}

func TestEmpty(t *testing.T) {
	tr := Build(nil, 0)
	if tr.Len() != 1 || len(tr.Children(Root)) != 0 {
		t.Fatal("empty tree has nodes")
	}
}

func BenchmarkBuild(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 390_000; i++ {
		sb.WriteString("packages/")
		sb.WriteString(strings.Repeat("x", i%7))
		sb.WriteString("/src/module")
		sb.WriteByte(byte('a' + i%26))
		sb.WriteString("/file")
		sb.WriteString(string(rune('0' + i%10)))
		sb.WriteString(".ts")
		sb.WriteByte(0)
	}
	in := []byte(sb.String())
	for b.Loop() {
		Build(in, 0)
	}
}
