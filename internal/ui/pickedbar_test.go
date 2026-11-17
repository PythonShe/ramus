package ui

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Several commits picked show a bar over the history with what acts on
// them: two compare, they cherry-pick onto the branch checked out, oldest
// first, and Clear lets them go.
func TestPickedBar(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("checkout", "-q", "-b", "side")
	r.Write("s1.txt", "1\n")
	one := r.Commit("side one")
	r.Write("s2.txt", "2\n")
	two := r.Commit("side two")
	r.Git("checkout", "-q", "main")
	r.Write("m.txt", "m\n")
	main := r.Commit("on main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("side two"))
	hv := &tb.hist
	pick := func(ids ...string) {
		hv.picked.Clear()
		for _, id := range ids {
			b, _ := hex.DecodeString(id)
			hv.picked.Add(string(b))
		}
		h.tt.Frame()
	}
	if h.tt.HasText("1 commit picked") {
		t.Fatal("a bar for one commit")
	}

	pick(one, two)
	h.until("the bar", h.shows("2 commits picked"))
	h.click("Compare")
	h.until("the comparison", func() bool { return tb.sel == "compare" && tb.cmp.d != nil && tb.cmp.d.Done })
	if d := tb.cmp.d; d.Files.Len() != 1 || string(d.Files.Path(0)) != "s2.txt" {
		t.Fatalf("compared %d files", d.Files.Len())
	}

	h.click("History")
	pick(one, two)
	h.until("the bar again", h.shows("2 commits picked"))
	h.click("Cherry-Pick")
	h.idle(tb, "the cherry-pick")
	if got := r.Git("log", "-3", "--format=%s"); got != "side two\nside one\non main\n" {
		t.Fatalf("main has %q", got)
	}

	// Three picked: no Compare; Clear leaves the chosen one alone.
	pick(one, two, main)
	h.until("three", h.shows("3 commits picked"))
	if _, ok := h.tt.AccessByRole(ui.RoleButton, "Compare"); ok {
		t.Fatal("three commits compared")
	}
	h.click("Clear")
	if hv.picked.Len() > 1 || h.tt.HasText("3 commits picked") {
		t.Fatalf("%d picked after Clear", hv.picked.Len())
	}
}

// A merge among the commits picked turns the bar's Cherry-Pick off.
func TestPickedBarMerge(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("checkout", "-q", "-b", "side")
	r.Write("s.txt", "s\n")
	r.Commit("on side")
	r.Git("checkout", "-q", "main")
	r.Write("m.txt", "m\n")
	r.Commit("on main")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.click("History")
	h.until("the history", h.shows("merge side"))
	hs := tb.s.History
	for k := range 2 {
		tb.hist.picked.Add(string(hs.OID(hs.Snap.Commit(k))))
	}
	h.until("the bar", h.shows("2 commits picked"))
	b, ok := h.tt.AccessByRole(ui.RoleButton, "Cherry-Pick")
	if !ok || !b.Disabled || !strings.Contains(b.Description, "A merge is among them") {
		t.Fatalf("Cherry-Pick %+v", b)
	}
}
