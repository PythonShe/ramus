package settings

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestKeeperKeepsTheLatest has saves asked for back to back, from two
// goroutines as two windows would: the file ends with the last of them,
// never an older one written after it.
func TestKeeperKeepsTheLatest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	k := NewKeeper(p)
	var mu sync.Mutex
	last := 0
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range 100 {
				mu.Lock()
				last++
				s := Defaults
				s.SnapshotDays = last
				k.Keep(s, func(err error) { t.Error(err) })
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	k.Wait()
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotDays != last {
		t.Fatalf("file has %d, the last kept was %d", got.SnapshotDays, last)
	}
}

// TestKeeperReportsFailure says why a write failed to whoever asked for
// it.
func TestKeeperReportsFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	k := NewKeeper(filepath.Join(blocker, "settings.json"))
	var got error
	k.Keep(Defaults, func(err error) { got = err })
	k.Wait()
	if got == nil {
		t.Fatal("no error for a folder that is a file")
	}
}
