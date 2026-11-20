package osfile

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// git commits an index or a ref by renaming its lock file over the old
// one, which must succeed while the old one is being read here.
func TestRenamedOverWhileOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index")
	want := bytes.Repeat([]byte("old"), 1000)
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lock := filepath.Join(dir, "index.lock")
	if err := os.WriteFile(lock, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameOver(lock, path); err != nil {
		t.Fatalf("rename over a file being read: %v", err)
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the open file's content changed")
	}
	if b, err := ReadFile(path, 3); err != nil || string(b) != "new" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []int{0, 1, 511, 512, 513, 100_000} {
		path := filepath.Join(dir, "f")
		want := bytes.Repeat([]byte{'x'}, n)
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile(path, int64(n))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%d bytes: read %d, %v", n, len(got), err)
		}
	}
	if _, err := ReadFile(filepath.Join(dir, "missing"), 1); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := Open(filepath.Join(dir, "missing", "x")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing folder: %v", err)
	}
	if f, err := Open(dir); !errors.Is(err, ErrNotRegular) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("a folder: %v", err)
	}
	if _, err := ReadFile(dir, 1<<20); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("a folder: %v", err)
	}
}

// A file is read up to its limit, and past it refused.
func TestReadFileLimit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadFile(file, 10); err != nil || string(b) != "0123456789" {
		t.Fatalf("a file of the limit: %q %v", b, err)
	}
	for _, limit := range []int64{9, 0} {
		if _, err := ReadFile(file, limit); !errors.Is(err, ErrTooLarge) {
			t.Errorf("a file past the limit %d: %v", limit, err)
		}
	}
}
