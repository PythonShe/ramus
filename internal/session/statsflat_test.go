package session

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
)

// The commits a count reads are held in one buffer, not one allocation
// each: a whole history of the Linux kernel is 1.5 million of them.
func TestStatsCommitsAreFlat(t *testing.T) {
	r := gittest.Init(t)
	const n = 3000
	var stream strings.Builder
	for i := range n {
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter A <a@example.com> %d +0000\ndata 2\nc\n", 1700000000+i)
		fmt.Fprintf(&stream, "M 644 inline f\ndata %d\n%d\n\n", len(fmt.Sprint(i))+1, i)
	}
	if out, err := r.Run([]byte(stream.String()), "fast-import", "--quiet"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	r.Git("commit-graph", "write", "--reachable")
	g, err := commitgraph.Open(filepath.Join(r.GitDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	head, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "main")))
	pos := []uint32{g.Lookup(head)}
	all := func(int64) bool { return true }
	ids, err := graphCommits(t.Context(), g, pos, all)
	if err != nil || ids.Len() != n {
		t.Fatalf("%d commits, %v", ids.Len(), err)
	}
	allocs := testing.AllocsPerRun(3, func() { graphCommits(t.Context(), g, pos, all) })
	if allocs > n/10 {
		t.Fatalf("%v allocations for %d commits", allocs, n)
	}
}
