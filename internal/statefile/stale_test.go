package statefile

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The temporary files a write gave up on are removed once older than
// any write could still be running; a running write's, the lock, the
// file itself and other files are left.
func TestRemoveStale(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * StaleAfter)
	put := func(name string, at time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		".settings.json.123456", ".settings.json.broken.42", // given up on
		".layout.json.987", // another file's
		".settings.json.lock", "settings.json", ".settings.json.notes", "other.123",
	} {
		put(name, old)
	}
	put(".settings.json.777", time.Now()) // a write running
	RemoveStale(filepath.Join(dir, "settings.json"))
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{".layout.json.987", ".settings.json.777", ".settings.json.lock", ".settings.json.notes", "other.123", "settings.json"}
	if !slices.Equal(left, want) {
		t.Fatalf("left %q, want %q", left, want)
	}
	RemoveStaleIn(dir)
	entries, _ = os.ReadDir(dir)
	if len(entries) != len(want)-1 {
		t.Fatalf("the folder's own temporary file not removed: %v", entries)
	}
}
