package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// A tool rewriting 5,000 files at once costs a status or two, and the status
// published has every file, none blank (GitHub Desktop #22619, #22596).
func TestBulkWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 10,000 files")
	}
	runner, logged := loggedRunner(t)
	r := gittest.Init(t)
	path := func(i int) string {
		return filepath.Join(r.Dir, "gen", strconv.Itoa(i%50), "f"+strconv.Itoa(i)+".txt")
	}
	write := func(text string) {
		for i := range 5000 {
			if err := os.MkdirAll(filepath.Dir(path(i)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path(i), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("x\n")
	r.Commit("many")
	m := make(mainThread, 256)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil })
	m.settle(t, s)
	statusRuns := func() int {
		n := 0
		for _, l := range logged() {
			if strings.Contains(l, "status --porcelain") {
				n++
			}
		}
		return n
	}
	// The times the watcher woke the refresher, for the bursts it saw:
	// a machine busy enough to stall the writer or the watcher past
	// Debounce splits the one burst into several.
	var mu sync.Mutex
	var woke []time.Time
	note := func() {
		mu.Lock()
		woke = append(woke, time.Now())
		mu.Unlock()
	}
	s.woke.Store(&note)
	before := statusRuns()
	last := s.State.Status
	var shown []int
	start := time.Now()
	write("y\n")
	wrote := time.Since(start)
	m.until(t, "every file", func() bool {
		if st := s.State.Status; st != last {
			last = st
			shown = append(shown, st.Len())
			for i := range st.Len() {
				if len(st.Path(i)) == 0 {
					t.Fatalf("entry %d of %d has no path", i, st.Len())
				}
			}
		}
		return last.Len() == 5000
	})
	took := time.Since(start)
	m.settle(t, s)
	runs := statusRuns() - before
	s.woke.Store(nil)
	mu.Lock()
	bursts := debounced(woke)
	mu.Unlock()
	t.Logf("5,000 files written in %v; %d status runs for %d bursts, statuses shown %v, all shown after %v",
		wrote.Round(time.Millisecond), runs, bursts, shown, took.Round(time.Millisecond))
	// A status per burst the watcher saw, and one more for a change that
	// came as the refresher was not there to take it; a write slower than
	// MaxDebounce takes two even unbroken. A status per batch of events
	// would be dozens.
	if limit := max(bursts+1, 2+int(wrote/MaxDebounce)); runs > limit {
		t.Fatalf("%d status runs for %d bursts", runs, bursts)
	}
}

// debounced is how many refreshes changes at the times at call for as
// the refresher debounces them: one for each run of changes none
// Debounce after the one before, and none MaxDebounce after the first.
func debounced(at []time.Time) int {
	n := 0
	for i := 0; i < len(at); n++ {
		first, prev := at[i], at[i]
		for i++; i < len(at) && at[i].Sub(prev) < Debounce && at[i].Sub(first) < MaxDebounce; i++ {
			prev = at[i]
		}
	}
	return n
}
