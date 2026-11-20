package statefile

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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
		".settings.json.123456", ".settings.json.broken.42", ".settings.json.broken-2.42", // given up on
		".layout.json.987", // another file's
		".settings.json.lock", "settings.json", ".settings.json.notes", "other.123",
		// the user's, in a form no write of the app's makes
		".settings.json.backup.20260101", ".settings.json.orig.1", ".settings.json.v2.swp.7",
		".settings.json.broken-.1", ".settings.json.broken-x.1", ".settings.json.broken.", ".settings.json.",
		"settings.json.20260101", ".settings.jsonx.1",
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
	want := []string{".layout.json.987", ".settings.json.", ".settings.json.777", ".settings.json.backup.20260101",
		".settings.json.broken-.1", ".settings.json.broken-x.1", ".settings.json.broken.", ".settings.json.lock",
		".settings.json.notes", ".settings.json.orig.1", ".settings.json.v2.swp.7", ".settings.jsonx.1",
		"other.123", "settings.json", "settings.json.20260101"}
	if want = asStored(want); !slices.Equal(left, want) {
		t.Fatalf("left %q, want %q", left, want)
	}
	// In a folder the app alone writes, every old temporary name goes.
	RemoveStaleIn(dir)
	entries, _ = os.ReadDir(dir)
	left = left[:0]
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want = []string{".settings.json.", ".settings.json.777", ".settings.json.broken.", ".settings.json.lock",
		".settings.json.notes", "other.123", "settings.json", "settings.json.20260101"}
	if want = asStored(want); !slices.Equal(left, want) {
		t.Fatalf("in the app's own folder left %q, want %q", left, want)
	}
}

// asStored is names as the file system keeps them, sorted: Windows drops
// the dots a name ends in, which leaves names no write makes either.
func asStored(names []string) []string {
	if runtime.GOOS != "windows" {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strings.TrimRight(n, ".")
	}
	slices.Sort(out)
	return out
}
