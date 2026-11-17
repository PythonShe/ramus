package ui

import (
	"image"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// farthestInk returns the colour in r that differs most from the colour
// beside it, the core of the text drawn there, and that colour beside it.
func farthestInk(img *image.RGBA, r ui.Rect) (got, bg [3]int) {
	b := img.RGBAAt(int(r.X)-2, int(r.Y+r.H/2))
	bg = [3]int{int(b.R), int(b.G), int(b.B)}
	best := -1
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			c := img.RGBAAt(x, y)
			d := absInt(int(c.R)-bg[0]) + absInt(int(c.G)-bg[1]) + absInt(int(c.B)-bg[2])
			if d > best {
				best, got = d, [3]int{int(c.R), int(c.G), int(c.B)}
			}
		}
	}
	return got, bg
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func rgb(c ui.Color) [3]int { return [3]int{int(c.R), int(c.G), int(c.B)} }

func rgbDist(a, b [3]int) int {
	return absInt(a[0]-b[0]) + absInt(a[1]-b[1]) + absInt(a[2]-b[2])
}

// The secondary text of a chosen row is drawn in AccentText, not in the
// muted grey, which is unreadable on the accent the row is painted in.
// The tester has no accessor for an element's colour, so this reads the
// pixels of the subject.
func TestChosenRowTextIsReadable(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("b", "2")
	r.Commit("a later subject")
	r.Git("branch", "old", "HEAD~1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.click("Branches and Tags")
	h.until("the branch", func() bool { return hasExact(h, "old") })

	// "one" is the subject of the commit old is at, shown by no other row.
	if onAccent(h, "one")() {
		t.Fatal("the row starts chosen")
	}
	h.click("old")
	h.until("the row chosen", onAccent(h, "one"))
	inkOnAccent(h, "one")
}

// onAccent reports whether the text s shows on a row painted in the
// accent, as a chosen row is.
func onAccent(h *harness, s string) func() bool {
	return func() bool {
		o, ok := h.tt.Find(s)
		if !ok {
			return false
		}
		_, bg := farthestInk(h.tt.Image(), o)
		return rgbDist(bg, rgb(ivoryTheme().Accent)) < 40
	}
}

// inkOnAccent fails unless the text s is drawn near AccentText, and
// nearer it than the muted grey.
func inkOnAccent(h *harness, s string) {
	h.t.Helper()
	th := ivoryTheme()
	o, _ := h.tt.Find(s)
	ink, _ := farthestInk(h.tt.Image(), o)
	want, muted := rgb(th.AccentText), rgb(th.TextMuted)
	if rgbDist(ink, want) > 120 || rgbDist(ink, muted) < rgbDist(ink, want) {
		h.t.Fatalf("%q drawn as %v, want near AccentText %v, not TextMuted %v", s, ink, want, muted)
	}
}

// The history's columns and a commit's files, a list's rows rather than
// a table's, take the same colours on a chosen row.
func TestChosenListRowTextIsReadable(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("b", "2")
	r.Commit("a later subject")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.click("History")
	short := strings.TrimSpace(r.Git("rev-parse", "--short=8", "HEAD~1"))
	h.until("the hash", func() bool { return hasExact(h, short) })
	if onAccent(h, short)() {
		t.Fatal("the row starts chosen")
	}
	h.click("one")
	h.until("the commit chosen", onAccent(h, short))
	inkOnAccent(h, short)

	h.click("a later subject")
	h.until("its file", func() bool { return hasExact(h, "b") })
	h.click("b")
	h.until("the file chosen", onAccent(h, "A"))
	inkOnAccent(h, "A")
}

// A cell asks its table whether its row is chosen for the colours to use.
func TestInkOnChosenRow(t *testing.T) {
	th := ui.LightTheme()
	sel := 1
	s := &tableState{}
	s.Selected = &sel
	if s.chosen(0) || !s.chosen(1) {
		t.Fatal("chosen does not follow Selected")
	}
	if plainInk(th).Muted != th.TextMuted {
		t.Fatal("a row not chosen changed its colours")
	}
}
