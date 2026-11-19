package ui

import (
	"strings"
	"testing"
)

// A bisect started in a terminal with terms of its own says which button
// marks which, and its Good and Bad mark with its terms, which git
// bisect good and bad refuse there.
func TestBisectWithItsOwnTerms(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	var commits []string
	for _, s := range []string{"a", "b", "c", "d", "e", "f"} {
		r.Write("n", s)
		commits = append(commits, r.Commit("step "+s))
	}
	r.Git("bisect", "start", "--term-old=fixed", "--term-new=broken")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the bar", h.shows("Bisecting with the terms broken and fixed: Bad marks a commit broken, Good marks it fixed"))
	h.click("Bad")
	h.idle(tb, "the mark")
	if got := strings.TrimSpace(r.Git("rev-parse", "refs/bisect/broken")); got != commits[5] {
		t.Fatalf("broken is %s, want %s", got, commits[5])
	}
	r.Git("bisect", "fixed", commits[0])
	h.until("bisecting", h.shows("left to test"))
	under := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	// git moves the refs, then HEAD: the bar can show the progress before
	// it has read the commit checked out.
	h.until("the commit under test", func() bool { return tb.bisect.state.Testing == under })
	h.click("Good")
	h.idle(tb, "the mark")
	if r.Git("for-each-ref", "--format=%(refname)", "refs/bisect/fixed-"+under) == "" {
		t.Fatalf("%s not marked fixed:\n%s", under, r.Git("bisect", "log"))
	}
}

// A bisect started with --no-checkout tests BISECT_HEAD and leaves HEAD
// where it was: the bar marks the commit under test, not HEAD.
func TestBisectNoCheckoutMarksTheCommitUnderTest(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for i := range 8 {
		r.Write("n", strings.Repeat("x", i+1))
		r.Commit("step " + string(rune('a'+i)))
	}
	r.Git("bisect", "start", "--no-checkout", "HEAD", "HEAD~7")
	under := strings.TrimSpace(r.Git("rev-parse", "BISECT_HEAD"))
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("History")
	h.until("bisecting", h.shows("left to test"))
	h.click("Good")
	h.idle(tb, "the mark")
	if r.Git("for-each-ref", "--format=%(refname)", "refs/bisect/good-"+under) == "" {
		t.Fatalf("%s not marked good:\n%s", under, r.Git("bisect", "log"))
	}
}
