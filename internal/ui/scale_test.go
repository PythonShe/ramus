package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/status"
)

// 30,000 changed files cost a frame what a few do: the list builds the
// rows on screen and the model is built off the main thread.
func TestManyChangesFrameTime(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))

	const n = 30000
	var b strings.Builder
	b.WriteString("# branch.oid " + strings.Repeat("0", 40) + "\x00# branch.head main\x00")
	blob := strings.Repeat("1", 40)
	for i := range n {
		fmt.Fprintf(&b, "1 .M N... 100644 100644 100644 %s %s src/d%03d/file%05d.go\x00", blob, blob, i%300, i)
	}
	st, err := status.Parse([]byte(b.String()), 20)
	if err != nil {
		t.Fatal(err)
	}
	tb := h.app.current()
	tb.s.State.Status = st
	h.until("the changes", h.shows(fmt.Sprintf("Changes (%d)", n)))
	h.until("the rows", h.shows("src/d000/file00000.go"))

	frames := func(what string, step func(i int)) time.Duration {
		var d []time.Duration
		for i := range 60 {
			step(i)
			start := time.Now()
			h.tt.Frame()
			d = append(d, time.Since(start))
		}
		slices.Sort(d)
		p50, p95 := d[len(d)/2], d[len(d)*95/100]
		t.Logf("%s, %d changed files: frame p50 %v, p95 %v", what, n, p50.Round(10*time.Microsecond), p95.Round(10*time.Microsecond))
		return p50
	}
	idle := frames("idle", func(int) {})
	at, ok := h.tt.Find("src/d000/file00000.go")
	if !ok {
		t.Fatal("first row not found")
	}
	x, y := at.X+at.W/2, at.Y+at.H/2
	scroll := frames("scrolling the list", func(int) { h.tt.Scroll(x, y, 0, 240) })
	if h.tt.HasText("src/d000/file00000.go") {
		t.Fatal("the list did not scroll")
	}
	// Generous bounds: the test runs at background priority beside others.
	if idle > 50*time.Millisecond || scroll > 50*time.Millisecond {
		t.Fatalf("frames take %v idle, %v scrolling", idle, scroll)
	}
}
