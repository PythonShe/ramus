package repo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// The ignored directories are the topmost of each ignored tree, under
// tracked and untracked folders alike; ignored files are not listed.
func TestIgnoredDirs(t *testing.T) {
	g := gittest.Init(t)
	g.Write(".gitignore", "build/\nnode_modules/\n*.o\n")
	g.Write("src/main.c", "int main;\n")
	g.Commit("one")
	for _, f := range []string{
		"build/out/a.o", "node_modules/x/y/index.js", "src/main.o", "src/node_modules/z.js",
		"loose/node_modules/q.js", "loose/file", "with space/build/x",
	} {
		g.Write(f, "x")
	}
	if err := os.MkdirAll(filepath.Join(g.Dir, "build2"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.IgnoredDirs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"build", "loose/node_modules", "node_modules", "src/node_modules", "with space/build"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	// Folders of ignored files only, none a rule ignores.
	g.Write(".gitignore", "*.o\n")
	if got, err := r.IgnoredDirs(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("got %q %v", got, err)
	}
}

// Output split anywhere reads the same; a name too long for a path, and
// names past the budget, are left out.
func TestDirLines(t *testing.T) {
	long := strings.Repeat("l", 5000)
	out := "a/\x00file\x00b c/d/\x00" + long + "/\x00e/\x00"
	for size := 1; size <= len(out); size += 7 {
		d := &dirLines{}
		for b := []byte(out); len(b) > 0; {
			n := min(size, len(b))
			d.Write(b[:n])
			b = b[n:]
		}
		if want := []string{"a", "b c/d", "e"}; !slices.Equal(d.dirs, want) {
			t.Fatalf("in writes of %d: %q", size, d.dirs)
		}
	}
	d := &dirLines{}
	name := strings.Repeat("n", 1000)
	for range ignoredBudget/1000 + 10 {
		d.Write([]byte(name + "/\x00"))
	}
	if d.n > ignoredBudget || len(d.dirs) == 0 {
		t.Fatalf("%d bytes kept in %d names", d.n, len(d.dirs))
	}
}

func TestTracksUnder(t *testing.T) {
	g := gittest.Init(t)
	g.Write(".gitignore", "dist/\n")
	g.Write("a", "1")
	g.Commit("one")
	g.Write("dist/app.js", "x")
	r, err := Open(context.Background(), testrun.Runner(t), g.Dir, trustAll)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	if r.TracksUnder(ctx, []string{"dist"}) || r.TracksUnder(ctx, nil) {
		t.Fatal("tracks under an ignored folder with nothing added")
	}
	g.Git("add", "-f", "dist/app.js")
	if !r.TracksUnder(ctx, []string{"other", "dist"}) {
		t.Fatal("a file added with -f not seen")
	}
}
