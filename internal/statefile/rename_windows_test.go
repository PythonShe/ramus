package statefile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteFileWaitsForAReader has the file held open, as an editor or a
// scanner does for a moment, while it is replaced: Go opens files without
// sharing their deletion, so the rename fails until the reader lets go,
// and the write waits for that rather than fail.
func TestWriteFileWaitsForAReader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := WriteFile(p, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.Close()
	}()
	if err := WriteFile(p, []byte(`{"theme": "dark"}`+"\n")); err != nil {
		t.Fatalf("write while the file was open: %v", err)
	}
	if data, _ := os.ReadFile(p); string(data) != `{"theme": "dark"}`+"\n" {
		t.Fatalf("file has %q", data)
	}
}
