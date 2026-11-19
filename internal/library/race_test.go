package library

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Changes made at once, as two repositories of a group opening, each
// save the library: the file ends with all of them, never a write made
// from an older state landing last.
func TestConcurrentSavesKeepEveryChange(t *testing.T) {
	for round := range 50 {
		p := filepath.Join(t.TempDir(), "library.json")
		l, _ := Load(p)
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() { l.Opened(fmt.Sprintf("/nowhere/%d", i), time.Now()) })
		}
		wg.Wait()
		back, err := Load(p)
		if n := len(back.Recent(100)); err != nil || n != 8 {
			t.Fatalf("round %d: the file has %d of 8 (%v)", round, n, err)
		}
	}
}
