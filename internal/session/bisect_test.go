package session

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// A bisect started with --no-checkout tests BISECT_HEAD, which it moves,
// and leaves HEAD where it was; one that checks out tests HEAD.
func TestLoadBisectNoCheckout(t *testing.T) {
	r := gittest.Init(t)
	var commits []string
	for i := range 8 {
		r.Write("n", strings.Repeat("x", i+1))
		commits = append(commits, r.Commit("step"))
	}
	s, m := openFor(t, r)
	load := func() BisectState {
		var got *BisectState
		s.LoadBisect(func(b BisectState, err error) {
			if err != nil {
				t.Error(err)
			}
			got = &b
		})
		m.until(t, "the bisect", func() bool { return got != nil })
		return *got
	}
	r.Git("bisect", "start", "--no-checkout", commits[7], commits[0])
	b := load()
	under := strings.TrimSpace(r.Git("rev-parse", "BISECT_HEAD"))
	if b.Testing != under || under == commits[7] {
		t.Fatalf("testing %q, want BISECT_HEAD %s", b.Testing, under)
	}
	r.Git("bisect", "reset")

	r.Git("bisect", "start", commits[7], commits[0])
	if b := load(); !b.Active || b.Testing != strings.TrimSpace(r.Git("rev-parse", "HEAD")) {
		t.Fatalf("a bisect that checks out: %+v", b)
	}
}
