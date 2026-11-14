package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tens of thousands of refs cost a frame what a few do: labels and the
// grouped list are built off the main thread, the list builds the rows on
// screen, and a folder of branches closes.
func TestManyRefs(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	var b strings.Builder
	b.WriteString("# pack-refs with: peeled fully-peeled sorted \n")
	for i := range 20000 {
		fmt.Fprintf(&b, "%s refs/heads/feature/%05d\n", head, i)
	}
	for i := range 10000 {
		fmt.Fprintf(&b, "%s refs/tags/v%05d\n", head, i)
	}
	if err := os.WriteFile(filepath.Join(r.GitDir(), "packed-refs"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	tb := h.app.current()
	if n := tb.s.State.Refs.Len(); n != 30001 {
		t.Fatalf("%d refs", n)
	}

	h.click("History")
	h.until("the labels", func() bool { return len(tb.hist.labels[string(tb.s.State.Head.OID)]) == 30001 })
	history := h.frameTime(11)

	h.click("Branches and Tags")
	h.until("the branches", func() bool { return len(tb.refs.rows) > 0 && hasExact(h, "feature/") })
	refs := h.frameTime(11)
	t.Logf("30,001 refs: history frame %v, branches frame %v", history.Round(10*time.Microsecond), refs.Round(10*time.Microsecond))
	if history > 50*time.Millisecond || refs > 50*time.Millisecond {
		t.Fatalf("frames take %v and %v", history, refs)
	}
	rows := len(tb.refs.rows)
	h.click("▾")
	h.until("the folder closed", func() bool { return len(tb.refs.rows) < rows })
}
