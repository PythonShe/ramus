package session

import (
	"runtime"
	"strconv"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// Browsing a repository for as long as one likes holds no more than its
// budget: commits and patches read past it push the oldest out, and the
// heap grows by no more than the answers kept, so their sizes are told
// honestly.
func TestMemoryBudget(t *testing.T) {
	r := gittest.Init(t)
	const commits, size = 24, 256 << 10
	line := strings.Repeat("x", 63) + "\n"
	for i := range commits {
		name := "f" + strconv.Itoa(i) + ".txt"
		r.Write(name, strings.Repeat(line, size/len(line)))
		r.Commit("add " + name)
	}
	s, m := openFor(t, r)
	const budget = 2 << 20
	s.SetAnswersBudget(budget)
	ids := strings.Fields(r.Git("rev-list", "HEAD"))

	browse := func() {
		for _, id := range ids {
			oid := mustHex(t, id)
			var d CommitDetail
			s.LoadCommit(oid, func(cd CommitDetail) { d = cd })
			m.until(t, "the commit", func() bool { return d.Done || d.Err != nil })
			if d.Err != nil || d.Files == nil || d.Files.Len() != 1 {
				t.Fatalf("commit %s: %v", id, d.Err)
			}
			got := false
			s.CommitPatch(d, []string{string(d.Files.Path(0))}, 0, 0, Sides{}, func(p PatchResult) {
				got = true
				if p.Err != nil {
					t.Error(p.Err)
				}
			})
			m.until(t, "the patch", func() bool { return got })
		}
	}
	heap := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}
	before := heap()
	browse()
	browse()
	after := heap()
	c := s.answers()
	if c.Bytes() > budget {
		t.Fatalf("answers hold %d bytes, over %d", c.Bytes(), budget)
	}
	if c.Len() == 0 || c.Len() >= 2*commits {
		t.Fatalf("%d answers kept of %d read", c.Len(), 2*commits)
	}
	// What is not an answer (a cat-file process's buffers) is small.
	if grew := int64(after) - int64(before); grew > budget+budget/2 {
		t.Fatalf("the heap grew %d bytes browsing, over the %d budget", grew, budget)
	}
	t.Logf("answers %d in %d bytes; heap %d -> %d", c.Len(), c.Bytes(), before, after)
}
