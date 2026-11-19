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
	if err := os.Rename(lock, path); err != nil {
		t.Fatalf("rename over a file being read: %v", err)
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the open file's content changed")
	}
	if b, err := ReadFile(path); err != nil || string(b) != "new" {
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
		got, err := ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%d bytes: read %d, %v", n, len(got), err)
		}
	}
	if _, err := ReadFile(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := Open(filepath.Join(dir, "missing", "x")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing folder: %v", err)
	}
	f, err := Open(dir)
	if err != nil {
		t.Fatalf("a directory: %v", err)
	}
	st, err := f.Stat()
	f.Close()
	if err != nil || !st.IsDir() {
		t.Fatalf("a directory: %v, %v", st, err)
	}
}
