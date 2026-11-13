package mmap

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	want := bytes.Repeat([]byte("ramus"), 4096)
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(m.Bytes(), want) {
		t.Fatal("mapped bytes differ from the file")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if m.Bytes() != nil {
		t.Fatal("Bytes after Close")
	}
}

func TestOpenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Bytes()) != 0 {
		t.Fatal("empty file mapped bytes")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDirectory(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("mapped a directory")
	}
}
