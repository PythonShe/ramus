package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// A tool rewriting 5,000 files at once costs one status, and the status
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
	m.pump(4 * Debounce)
	statusRuns := func() int {
		n := 0
		for _, l := range logged() {
			if strings.Contains(l, "status --porcelain") {
				n++
			}
		}
		return n
	}
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
	m.pump(4 * Debounce)
	runs := statusRuns() - before
	t.Logf("5,000 files written in %v; %d status runs, statuses shown %v, all shown after %v",
		wrote.Round(time.Millisecond), runs, shown, took.Round(time.Millisecond))
	// A write slower than MaxDebounce may take two.
	if limit := 1 + int(wrote/MaxDebounce); runs > limit {
		t.Fatalf("%d status runs for one burst", runs)
	}
}
