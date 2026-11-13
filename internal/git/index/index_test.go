package index

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// oracle reads the index through git ls-files --stage --debug.
func oracle(t *testing.T, r *gittest.Repo) []string {
	// --debug lines are not NUL-separated; paths here need no quoting.
	out := r.Git("-c", "core.quotePath=false", "ls-files", "--stage", "--debug")
	var rows []string
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	for i := 0; i < len(lines); i += 6 {
		head := lines[i] // mode oid stage\tpath
		field := func(l string) []string { return strings.Fields(l) }
		ctime := field(lines[i+1])[1]
		mtime := field(lines[i+2])[1]
		dev, ino := field(lines[i+3])[1], field(lines[i+3])[3]
		uid, gid := field(lines[i+4])[1], field(lines[i+4])[3]
		size, flags := field(lines[i+5])[1], field(lines[i+5])[3]
		rows = append(rows, strings.Join([]string{head, ctime, mtime, dev, ino, uid, gid, size, flags}, " "))
	}
	return rows
}

func ours(x *Index) []string {
	var rows []string
	for i := range x.Entries {
		e := &x.Entries[i]
		// git prints its in-memory flags in hex: no name length, and the
		// extended flags shifted up.
		flags := uint32(e.Flags&^0x0fff) | uint32(e.ExtFlags)<<16
		rows = append(rows, strings.Join([]string{
			fmt.Sprintf("%06o %x %d\t%s", e.Mode, x.OID(i), e.Stage(), x.Path(i)),
			fmt.Sprintf("%d:%d", e.CtimeSec, e.CtimeNsec), fmt.Sprintf("%d:%d", e.MtimeSec, e.MtimeNsec),
			strconv.Itoa(int(e.Dev)), strconv.Itoa(int(e.Ino)), strconv.Itoa(int(e.UID)), strconv.Itoa(int(e.GID)),
			strconv.Itoa(int(e.Size)), strconv.FormatUint(uint64(flags), 16),
		}, " "))
	}
	return rows
}

func fixture(t *testing.T) *gittest.Repo {
	r := gittest.Init(t)
	for i := 0; i < 40; i++ {
		r.Write(fmt.Sprintf("dir%d/sub/file-%03d.txt", i%4, i), strings.Repeat("x", i))
	}
	r.Write("caf\u00e9/\u65e5\u672c\u8a9e.md", "unicode")
	r.Write("a-very-long-directory-name-to-push-past-padding/and-a-long-file-name.go", "go")
	r.Commit("files")
	return r
}

func compare(t *testing.T, r *gittest.Repo, want func(*Index)) {
	t.Helper()
	x, err := ReadFile(filepath.Join(r.GitDir(), "index"), 20)
	if err != nil {
		t.Fatal(err)
	}
	got, exp := ours(x), oracle(t, r)
	if len(got) != len(exp) {
		t.Fatalf("%d entries, git %d", len(got), len(exp))
	}
	for i := range got {
		if got[i] != exp[i] {
			t.Fatalf("entry %d:\n got %s\nwant %s", i, got[i], exp[i])
		}
	}
	if want != nil {
		want(x)
	}
}

func TestVersions(t *testing.T) {
	for _, v := range []int{2, 3, 4} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			r := fixture(t)
			r.Git("update-index", "--index-version", fmt.Sprint(v))
			if v >= 3 {
				r.Git("update-index", "--skip-worktree", "dir0/sub/file-000.txt")
				r.Write("new.txt", "n")
				r.Git("add", "-N", "new.txt")
			}
			compare(t, r, func(x *Index) {
				if x.Version != v || !x.Checksum {
					t.Errorf("version %d checksum %v", x.Version, x.Checksum)
				}
			})
		})
	}
}

func TestConflictStages(t *testing.T) {
	r := gittest.Init(t)
	r.Write("f", "base\n")
	r.Commit("base")
	r.Git("checkout", "-q", "-b", "other")
	r.Write("f", "other\n")
	r.Commit("other")
	r.Git("checkout", "-q", "main")
	r.Write("f", "main\n")
	r.Commit("main")
	r.Run(nil, "merge", "-q", "other")
	compare(t, r, nil)
}

func TestSkipHash(t *testing.T) {
	r := fixture(t)
	r.Git("-c", "index.skipHash=true", "update-index", "--really-refresh")
	r.Write("x", "x")
	r.Git("-c", "index.skipHash=true", "add", "x")
	compare(t, r, func(x *Index) {
		if x.Checksum {
			t.Error("checksum reported on a skipHash index")
		}
	})
}

func TestSplitAndSparse(t *testing.T) {
	r := fixture(t)
	r.Git("update-index", "--split-index")
	x, err := ReadFile(filepath.Join(r.GitDir(), "index"), 20)
	if err != nil || !x.Split() {
		t.Fatalf("split index not seen: %v", err)
	}
	s := fixture(t)
	s.Git("sparse-checkout", "init", "--cone")
	s.Git("config", "index.sparse", "true")
	s.Git("sparse-checkout", "set", "dir1")
	x, err = ReadFile(filepath.Join(s.GitDir(), "index"), 20)
	if err != nil || !x.Sparse() {
		t.Fatalf("sparse index not seen: %v", err)
	}
	dirs := 0
	for i := range x.Entries {
		if x.Entries[i].SparseDir() {
			dirs++
		}
	}
	if dirs == 0 {
		t.Error("no sparse directory entries")
	}
}

func TestDamaged(t *testing.T) {
	r := fixture(t)
	good, _ := os.ReadFile(filepath.Join(r.GitDir(), "index"))
	cases := map[string]func([]byte) []byte{
		"short":     func(b []byte) []byte { return b[:10] },
		"signature": func(b []byte) []byte { b[0] = 'X'; return b },
		"version":   func(b []byte) []byte { b[7] = 9; return b },
		"count":     func(b []byte) []byte { b[8] = 0x7f; return b },
		"cut":       func(b []byte) []byte { return b[:len(b)/2] },
	}
	for name, f := range cases {
		if _, err := Parse(f(append([]byte(nil), good...)), 20); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("DIRC\x00\x00\x00\x02\x00\x00\x00\x00" + strings.Repeat("\x00", 20)))
	f.Fuzz(func(t *testing.T, b []byte) {
		x, err := Parse(b, 20)
		if err != nil {
			return
		}
		for i := range x.Entries {
			x.Path(i)
			x.OID(i)
		}
		x.Split()
		x.Sparse()
	})
}

// TestMonorepo reads the 390k-entry index of the generated monorepo and
// compares it with git, when RAMUS_FIXTURES is set.
func TestMonorepo(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir := filepath.Join(gittest.FixturesDir(t), "monorepo.noindex")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	r := gittest.Open(t, dir)
	start := time.Now()
	x, err := ReadFile(filepath.Join(dir, ".git", "index"), 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d entries, version %d, read in %v", len(x.Entries), x.Version, time.Since(start))
	exp := strings.Split(strings.TrimSuffix(r.Git("ls-files", "--stage", "-z"), "\x00"), "\x00")
	if len(exp) != len(x.Entries) {
		t.Fatalf("%d entries, git %d", len(x.Entries), len(exp))
	}
	for i, line := range exp {
		got := fmt.Sprintf("%06o %x %d\t%s", x.Entries[i].Mode, x.OID(i), x.Entries[i].Stage(), x.Path(i))
		if got != line {
			t.Fatalf("entry %d:\n got %q\nwant %q", i, got, line)
		}
	}
}

// A version 4 index whose entries each keep a long prefix and add a byte
// is small on disk and huge in paths; it is refused, not allocated.
func TestPrefixAmplification(t *testing.T) {
	const n = 5000
	var b []byte
	b = append(b, "DIRC"...)
	b = binary.BigEndian.AppendUint32(b, 4)
	b = binary.BigEndian.AppendUint32(b, n)
	fixed := make([]byte, 40+20+2)
	for i := 0; i < n; i++ {
		b = append(b, fixed...)
		b = append(b, 0) // strip nothing
		if i == 0 {
			b = append(b, bytes.Repeat([]byte("p"), 60000)...)
		} else {
			b = append(b, 'x')
		}
		b = append(b, 0)
	}
	b = append(b, make([]byte, 20)...)
	if _, err := Parse(b, 20); !errors.Is(err, ErrFormat) {
		t.Fatalf("parsed %d bytes into %d MB of paths: %v", len(b), n*60000>>20, err)
	}
}
