package repo

import (
	"context"
	"slices"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// The files of the folder alone, the ignored left out, and past the bound
// only counted.
func TestUntrackedUnder(t *testing.T) {
	g := gittest.Init(t)
	g.Write(".gitignore", "*.log\n")
	g.Write("a", "1")
	g.Commit("one")
	for _, f := range []string{"new dir/b.txt", "new dir/deep/c.txt", "new dir/x.log", "new dirt/d.txt", "other/e.txt"} {
		g.Write(f, "x")
	}
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.UntrackedUnder(context.Background(), "new dir/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"new dir/b.txt", "new dir/deep/c.txt"}; !slices.Equal(got.Paths, want) || got.More != 0 {
		t.Fatalf("got %q and %d more\nwant %q", got.Paths, got.More, want)
	}
	got, err = r.UntrackedUnder(context.Background(), "new dir/", 1)
	if err != nil || len(got.Paths) != 1 || got.More != 1 {
		t.Fatalf("bound of 1: %q and %d more, %v", got.Paths, got.More, err)
	}
}

// Output split anywhere reads the same.
func TestNameLines(t *testing.T) {
	out := "a\x00b c/d\x00e\x00f\x00"
	for size := 1; size <= len(out); size++ {
		l := &nameLines{max: 2}
		for b := []byte(out); len(b) > 0; {
			n := min(size, len(b))
			l.Write(b[:n])
			b = b[n:]
		}
		if want := []string{"a", "b c/d"}; !slices.Equal(l.names, want) || l.more != 2 {
			t.Fatalf("in writes of %d: %q and %d more", size, l.names, l.more)
		}
	}
}
