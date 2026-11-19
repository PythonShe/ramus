package diff

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// A commit's counts line up with its list, renames and binary files
// included, as git diff --stat reads them.
func TestCommitLines(t *testing.T) {
	r := gittest.Init(t)
	body := strings.Repeat("shared line\n", 30)
	r.Write("old name", "v1\n"+body)
	r.Write("gone", "a\nb\n")
	r.Write("pic", "\x00\x01")
	r.Write("edit", "1\n2\n3\n")
	r.Commit("one")
	r.Git("mv", "old name", "new name")
	r.Write("new name", "v2\n"+body+"more\n")
	r.Git("rm", "-q", "gone")
	r.Write("pic", "\x00\x02")
	r.Write("edit", "1\ntwo\n3\nfour")
	r.Write("new", "x\ny\nz\n")
	r.Commit("two")
	run := testrun.Runner(t)
	a, b := oid(t, r, "HEAD~"), oid(t, r, "HEAD")
	raw, _, err := Renames(context.Background(), run, r.GitDir(), 20, a, b)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := CommitLines(context.Background(), run, r.GitDir(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Lines{
		"new name": {Added: 2, Deleted: 1, Known: true},
		"gone":     {Deleted: 2, Known: true},
		"pic":      {Binary: true, Known: true},
		"edit":     {Added: 2, Deleted: 1, Known: true},
		"new":      {Added: 3, Known: true},
	}
	got := ns.ForRaw(raw)
	if len(got) != raw.Len() || raw.Len() != len(want) {
		t.Fatalf("%d counts for %d files", len(got), raw.Len())
	}
	for i, l := range got {
		if w := want[string(raw.Path(i))]; l != w {
			t.Errorf("%s: %+v, want %+v", raw.Path(i), l, w)
		}
	}
	if tot := Total(got); tot != (Lines{Added: 7, Deleted: 4, Known: true}) {
		t.Errorf("total %+v", tot)
	}
	// A rename is found by both its paths only.
	if l := ns.Find([]byte("new name"), []byte("new name")); l.Known {
		t.Errorf("rename found by its new path alone: %+v", l)
	}
}

// The work tree's counts, staged and not, match git's; a path past the
// ones asked for is not counted.
func TestWorkTreeLines(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1\n2\n")
	r.Write("b", "1\n")
	r.Write("m", strings.Repeat("same\n", 20))
	r.Commit("one")
	r.Write("a", "1\n2\n3\n")
	r.Git("add", "a")
	r.Write("a", "0\n2\n3\n")
	r.Write("b", "")
	r.Git("mv", "m", "moved")
	ctx := context.Background()
	run := testrun.Runner(t)

	staged, err := WorkTreeLines(ctx, run, r.GitDir(), r.Dir, true, "HEAD", true, []string{"a", "m", "moved"})
	if err != nil {
		t.Fatal(err)
	}
	if l := staged.Find([]byte("a"), []byte("a")); l != (Lines{Added: 1, Known: true}) {
		t.Errorf("staged a %+v", l)
	}
	if l := staged.Find([]byte("m"), []byte("moved")); l != (Lines{Known: true}) {
		t.Errorf("staged rename %+v", l)
	}
	unstaged, err := WorkTreeLines(ctx, run, r.GitDir(), r.Dir, false, "", false, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if l := unstaged.Find([]byte("a"), []byte("a")); l != (Lines{Added: 1, Deleted: 1, Known: true}) {
		t.Errorf("unstaged a %+v", l)
	}
	if l := unstaged.Find([]byte("b"), []byte("b")); l.Known {
		t.Errorf("b counted unasked: %+v", l)
	}
	all, err := WorkTreeLines(ctx, run, r.GitDir(), r.Dir, false, "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l := all.Find([]byte("b"), []byte("b")); l != (Lines{Deleted: 1, Known: true}) {
		t.Errorf("unstaged b %+v", l)
	}
	// The index stays as git left it.
	if _, err := os.Stat(filepath.Join(r.GitDir(), "index.lock")); err == nil {
		t.Error("index left locked")
	}

	// Paths past what a Windows command line holds, counted as Windows
	// is given them, quotes and all, are not named: every change is
	// counted instead, b with a.
	for _, c := range []struct {
		name string
		size int
	}{{"long-name-", 26 << 10}, {`q"q q"q `, 20 << 10}} {
		paths := []string{"a"}
		size := 0
		for i := 0; size < c.size; i++ {
			p := strings.Repeat(c.name, 3) + strconv.Itoa(i)
			paths = append(paths, p)
			size += len(p) + 1
		}
		many, err := WorkTreeLines(ctx, run, r.GitDir(), r.Dir, false, "", false, paths)
		if err != nil {
			t.Fatal(err)
		}
		if l := many.Find([]byte("b"), []byte("b")); !l.Known {
			t.Errorf("%d bytes of paths like %q named to git", size, c.name)
		}
	}
}

func TestCountText(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Lines
	}{
		{"", Lines{Known: true}},
		{"a\nb\n", Lines{Added: 2, Known: true}},
		{"a\nb", Lines{Added: 2, Known: true}},
		{"a\x00b\n", Lines{Binary: true, Known: true}},
		{strings.Repeat("x", firstFew) + "\x00", Lines{Added: 1, Known: true}},
	} {
		if got := CountText([]byte(c.in)); got != c.want {
			t.Errorf("%.20q: %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseNumStatRefusesNonsense(t *testing.T) {
	for _, in := range []string{"1\t2", "1\t2\tp", "x\t2\tp\x00", "1\t\x00a\x00", "-\t3\tp\x00"} {
		if _, err := ParseNumStat([]byte(in)); err == nil {
			t.Errorf("%q parsed", in)
		}
	}
}
