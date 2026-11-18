package repo

import (
	"context"
	"slices"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// The files of each folder alone, the ignored left out, and past the
// bounds only counted.
func TestUntrackedUnder(t *testing.T) {
	g := gittest.Init(t)
	g.Write(".gitignore", "*.log\n")
	g.Write("a", "1")
	g.Commit("one")
	for _, f := range []string{"new dir/b.txt", "new dir/deep/c.txt", "new dir/x.log", "new dirt/d.txt", "other/e.txt", "z/f.txt"} {
		g.Write(f, "x")
	}
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dirs := []string{"z/", "new dir/", "new dirt/"}
	got, err := r.UntrackedUnder(context.Background(), dirs, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"new dir/": {"new dir/b.txt", "new dir/deep/c.txt"}, "new dirt/": {"new dirt/d.txt"}, "z/": {"z/f.txt"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for d, w := range want {
		if !slices.Equal(got[d].Paths, w) || got[d].More != 0 {
			t.Fatalf("%s: %q and %d more\nwant %q", d, got[d].Paths, got[d].More, w)
		}
	}
	got, err = r.UntrackedUnder(context.Background(), dirs, 1, 100)
	if err != nil || len(got["new dir/"].Paths) != 1 || got["new dir/"].More != 1 {
		t.Fatalf("bound of 1 each: %v, %v", got, err)
	}
	got, err = r.UntrackedUnder(context.Background(), dirs, 10, 2)
	if err != nil || len(got["new dir/"].Paths) != 2 || got["new dirt/"].More != 1 || got["z/"].More != 1 {
		t.Fatalf("bound of 2 in all: %v, %v", got, err)
	}
}

// Output split anywhere reads the same.
func TestEachLines(t *testing.T) {
	out := "a/1\x00a/b c/d\x00a/e\x00ab/f\x00b/g\x00r/\x00"
	for size := 1; size <= len(out); size++ {
		l := &eachLines{dirs: []string{"a/", "b/", "r/"}, each: 2, left: 100, out: map[string]UntrackedFiles{}}
		for b := []byte(out); len(b) > 0; {
			n := min(size, len(b))
			l.Write(b[:n])
			b = b[n:]
		}
		a, b := l.out["a/"], l.out["b/"]
		if !slices.Equal(a.Paths, []string{"a/1", "a/b c/d"}) || a.More != 1 || !slices.Equal(b.Paths, []string{"b/g"}) || !slices.Equal(l.out["r/"].Paths, []string{"r/"}) || len(l.out) != 3 {
			t.Fatalf("in writes of %d: %v", size, l.out)
		}
	}
}
