package write

import (
	"strings"
	"testing"
)

// A mark of the commit under test, run once the bisect moved on to
// another, as a second click queued behind the first, marks nothing: in
// a bisect that checks commits out and in one with --no-checkout.
func TestBisectMarkTestingRefusesOnceMovedOn(t *testing.T) {
	for _, noCheckout := range []bool{false, true} {
		h := newHarness(t)
		var commits []string
		for i := range 8 {
			h.r.Write("n", strings.Repeat("x", i+1))
			commits = append(commits, h.r.Commit("step"))
		}
		args := []string{"bisect", "start"}
		if noCheckout {
			args = append(args, "--no-checkout")
		}
		h.r.Git(append(args, commits[7], commits[0])...)
		under := h.rev("HEAD")
		if noCheckout {
			under = h.rev("BISECT_HEAD")
		}
		first, err := BisectMarkTesting(h.w, BisectBad, under)
		if err != nil {
			t.Fatal(err)
		}
		second, _ := BisectMarkTesting(h.w, BisectGood, under)
		h.do(first, nil, true)
		h.do(second, nil, false)
		if log := h.r.Git("bisect", "log"); strings.Contains(log, "# good: ["+under) {
			t.Fatalf("no-checkout %v: marked good after bad:\n%s", noCheckout, log)
		}
		h.do(BisectReset(h.w), nil, true)
	}
}
