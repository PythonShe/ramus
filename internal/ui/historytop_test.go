package ui

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// The history list is keyed by commit, and newest first: commits made or
// fetched land above its top. Showing its top, it shows them; scrolled
// down, it keeps the commit the user scrolled to at its top. Either way
// the commit chosen stays chosen, and a menu open on a row acts on that
// row's commit, though it moved meanwhile.
func TestHistoryShowsWhatLandsAboveItsTop(t *testing.T) {
	const n = 200
	for _, land := range []struct {
		what string
		// do lands commits above the top and returns the newest's id and
		// subject.
		do func(r *gittest.Repo) (string, string)
	}{
		{"a commit", func(r *gittest.Repo) (string, string) {
			r.Write("g.txt", "g\n")
			return r.CommitAt("made on top", 1800000000), "made on top"
		}},
		{"a fetch", func(r *gittest.Repo) (string, string) {
			var b strings.Builder
			for i := range 3 {
				fmt.Fprintf(&b, "commit refs/remotes/origin/main\ncommitter F <f@example.com> %d +0000\ndata 10\nfetched %d\n", 1800000000+i, i)
				if i == 0 {
					b.WriteString("from refs/heads/main^0\n")
				}
				b.WriteString("\n")
			}
			if out, err := r.Run([]byte(b.String()), "fast-import", "--quiet"); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			return strings.TrimSpace(r.Git("rev-parse", "origin/main")), "fetched 2"
		}},
	} {
		for _, scrolled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, scrolled %v", land.what, scrolled), func(t *testing.T) {
				h := newHarness(t)
				tb, r := longHistory(t, h, n)
				hv := &tb.hist
				top := func() int { first, _ := hv.list.Visible(); return first }
				oid := func(row int) string {
					hs := tb.s.History
					return hex.EncodeToString(hs.OID(hs.Snap.Commit(row)))
				}
				// Rows are newest first, over the repository's first commit.
				subject := func(row int) string { return fmt.Sprintf("commit %d", n-1-row) }
				if scrolled {
					at, ok := h.tt.Find("Commits")
					if !ok {
						t.Fatal("no list of commits")
					}
					h.tt.Wheel(at.X+at.W/2, at.Y+at.H/2, 0, 300)
					// The wheel's scroll eases on the clock.
					h.tt.Advance(time.Second)
					if top() == 0 {
						t.Fatal("the list did not scroll")
					}
				}
				first := top()
				h.until("the rows' text", h.shows(subject(first+3)))
				topID := oid(first)
				// A right click chooses its row, and opens its menu.
				menuID := oid(first + 3)
				if err := h.tt.RightClick(subject(first + 3)); err != nil {
					t.Fatal(err)
				}
				h.until("chosen", func() bool { return hv.chosen == first+3 })

				head, newest := land.do(r)
				h.until(land.what, func() bool {
					hs := tb.s.History
					return hs.For == tb.s.State.Refs && hs.Snap != nil && oid(0) == head
				})
				// Any slide to the new top, done.
				h.tt.Advance(time.Second)
				h.tt.Frame()

				if scrolled {
					if got := oid(top()); got != topID {
						t.Errorf("scrolled to %s at row %d; now row %d, %s, is at the top", topID[:10], first, top(), got[:10])
					}
				} else if top() != 0 || !h.tt.HasText(newest) {
					t.Errorf("the list showed its top; with %s above it, it shows row %d, %q shown %v", land.what, top(), newest, h.tt.HasText(newest))
				}
				if hv.chosen < 0 || oid(hv.chosen) != menuID {
					t.Errorf("chosen row %d, not the commit chosen, %s", hv.chosen, menuID[:10])
				}
				if err := h.tt.ChooseMenuItem("Copy Hash"); err != nil {
					t.Fatalf("%v: %q", err, h.tt.Menu())
				}
				h.tt.Frame()
				if got := h.tt.Clipboard(); got != menuID {
					t.Errorf("Copy Hash on %s copied %q, not %s", subject(first+3), got, menuID)
				}
			})
		}
	}
}
