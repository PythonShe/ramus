package tree

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func start(t testing.TB, r *gittest.Repo) *catfile.Process {
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func oid(t testing.TB, r *gittest.Repo, rev string) []byte {
	b, err := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", rev)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// format prints changes as git diff-tree -r --raw -z --no-renames does.
func format(c *Changes) string {
	var b strings.Builder
	for i := 0; i < c.Len(); i++ {
		om, oo := c.Old(i)
		nm, no := c.New(i)
		fmt.Fprintf(&b, ":%06o %06o %x %x %c\x00%s\x00", om, nm, oo, no, c.Status(i), c.Path(i))
	}
	return b.String()
}

func check(t *testing.T, r *gittest.Repo, p *catfile.Process, a, b string) {
	t.Helper()
	var oa []byte
	if a != "" {
		oa = oid(t, r, a+"^{tree}")
	}
	c, err := Diff(p, 20, oa, oid(t, r, b+"^{tree}"))
	if err != nil {
		t.Fatal(err)
	}
	base := a
	if base == "" {
		base = "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // the empty tree
	}
	want := r.Git("diff-tree", "-r", "--raw", "-z", "--no-renames", "--no-abbrev", base, b)
	if got := format(c); got != want {
		t.Fatalf("%s..%s\n got %q\nwant %q", a, b, got, want)
	}
}

func TestDiff(t *testing.T) {
	r := gittest.Init(t)
	files := map[string]string{
		"a.c": "1", "a/b": "1", "a/c/d": "1", "a-b": "1", "z": "1", "deep/1/2/3/4/f": "1",
		"exec": "1", "link": "x", "gone/x": "1", "gone/y/z": "1", "file2dir": "1", "dir2file/x": "1",
	}
	for p, c := range files {
		r.Write(p, c)
	}
	os.Remove(filepath.Join(r.Dir, "link"))
	os.Symlink("a.c", filepath.Join(r.Dir, "link"))
	r.Commit("one")
	r.Write("a/c/d", "2")
	r.Write("deep/1/2/3/4/f", "2")
	r.Write("deep/1/new", "x")
	os.Chmod(filepath.Join(r.Dir, "exec"), 0o755)
	os.Remove(filepath.Join(r.Dir, "link"))
	r.Write("link", "now a file")
	os.RemoveAll(filepath.Join(r.Dir, "gone"))
	os.Remove(filepath.Join(r.Dir, "file2dir"))
	r.Write("file2dir/inside", "1")
	os.RemoveAll(filepath.Join(r.Dir, "dir2file"))
	r.Write("dir2file", "1")
	r.Write("new/tree/f", "1")
	r.Git("add", "-A")
	r.Commit("two")
	p := start(t, r)
	check(t, r, p, "HEAD~", "HEAD")
	check(t, r, p, "HEAD", "HEAD~")
	check(t, r, p, "", "HEAD")
	check(t, r, p, "HEAD", "HEAD")
}

func TestGitlink(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	sub := r.Git("rev-parse", "HEAD")
	r.Git("update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(sub)+",mod")
	r.CommitIndex("gitlink")
	r.Commit("two")
	r.Git("update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(r.Git("rev-parse", "HEAD"))+",mod")
	r.CommitIndex("bump")
	p := start(t, r)
	check(t, r, p, "HEAD~2", "HEAD")
	check(t, r, p, "HEAD~", "HEAD")
}

func TestKernel(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "linux")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	r := gittest.Open(t, dir)
	p := start(t, r)
	// A merge window's worth of changes, and the first commit after a tag.
	for _, pair := range [][2]string{{"v6.6", "v6.7"}, {"v6.7~", "v6.7"}} {
		check(t, r, p, pair[0], pair[1])
	}
}

func FuzzIter(f *testing.F) {
	f.Add([]byte("100644 a\x00aaaaaaaaaaaaaaaaaaaa40000 b\x00bbbbbbbbbbbbbbbbbbbb"))
	f.Add([]byte("100644 \x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		it := NewIter(data, 20)
		for e, ok := it.Next(); ok; e, ok = it.Next() {
			if len(e.OID) != 20 || len(e.Name) == 0 {
				t.Fatalf("entry %+v", e)
			}
		}
	})
}

// BenchmarkKernelRelease compares the trees of two kernel releases, some
// 14,000 changed paths.
func BenchmarkKernelRelease(b *testing.B) {
	r := gittest.Open(b, filepath.Join(gittest.FixturesDir(b), "linux"))
	p := start(b, r)
	a, c := oid(b, r, "v6.6^{tree}"), oid(b, r, "v6.7^{tree}")
	for b.Loop() {
		ch, err := Diff(p, 20, a, c)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(ch.Len()), "paths")
	}
}
