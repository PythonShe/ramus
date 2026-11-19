package statefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFileIfLeavesAChangedFile has the file change before the
// rename: the write fails with ErrChanged, the file keeps the change, and
// no temporary file is left beside it.
func TestWriteFileIfLeavesAChangedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := WriteFile(p, []byte("1\n")); err != nil {
		t.Fatal(err)
	}
	err := WriteFileIf(p, []byte("2\n"), func() bool {
		os.WriteFile(p, []byte("edited\n"), 0o600)
		return false
	})
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if data, _ := os.ReadFile(p); string(data) != "edited\n" {
		t.Fatalf("file has %q", data)
	}
	if names, _ := os.ReadDir(dir); len(names) != 1 {
		t.Fatalf("left beside the file: %v", names)
	}
}

// TestKeptAsideUnderAFreeName keeps two unreadable files in turn: the
// second takes the next name, not the first's.
func TestKeptAsideUnderAFreeName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	var kept []string
	for _, content := range []string{"{", "[["} {
		os.WriteFile(p, []byte(content), 0o600)
		e := Unreadable(p, errors.New("bad"))
		if e.KeepErr != nil {
			t.Fatal(e.KeepErr)
		}
		kept = append(kept, e.KeptAs)
	}
	if kept[0] != p+".broken" || kept[1] != p+".broken-2" {
		t.Fatalf("kept as %v", kept)
	}
	if data, _ := os.ReadFile(kept[0]); string(data) != "{" {
		t.Fatalf("first kept file has %q", data)
	}
}
