package pathsafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A file is read up to its limit, and past it refused.
func TestReadFileLimit(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("0123456789"), 0o644)
	if b, err := ReadFile(file, 10); err != nil || string(b) != "0123456789" {
		t.Fatalf("a file of the limit: %q %v", b, err)
	}
	if _, err := ReadFile(file, 9); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a file past the limit: %v", err)
	}
	if _, err := ReadFile(dir, 10); err == nil {
		t.Error("read a directory")
	}
}
