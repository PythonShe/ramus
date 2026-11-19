package ui

import "testing"

// A bisect started in a terminal with terms of its own says so, and
// offers no Good or Bad, which git refuses there; Skip and End work.
func TestBisectWithItsOwnTerms(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	var commits []string
	for _, s := range []string{"a", "b", "c", "d"} {
		r.Write("n", s)
		commits = append(commits, r.Commit("step "+s))
	}
	r.Git("bisect", "start", "--term-old=fixed", "--term-new=broken", commits[3], commits[0])
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	h.click("History")
	h.until("the bar", h.shows("Bisecting with the terms broken and fixed: mark commits in a terminal"))
	for _, name := range []string{"Good", "Bad"} {
		if b, ok := h.tt.Access(name); !ok || !b.Disabled {
			t.Errorf("%s is offered: %+v", name, b)
		}
	}
	for _, name := range []string{"Skip", "End Bisect"} {
		if b, ok := h.tt.Access(name); !ok || b.Disabled {
			t.Errorf("%s is not offered: %+v", name, b)
		}
	}
}
