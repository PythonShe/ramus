package ui

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The branch button in the tab bar names the branch; its panel fetches,
// filters the branches by what is typed, and a click or Enter switches.
func TestBranchPicker(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("branch", "remote-only")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("branch", "feature/alpha")
	r.Git("branch", "feature/beta")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	up.Git("branch", "pushed-since")

	h.click("Branch main")
	shown := func(want ...string) func() bool {
		return func() bool {
			var got []string
			for _, r := range tb.pick.rows {
				got = append(got, r.ref)
			}
			return slices.Equal(got, want)
		}
	}
	// Opening it fetches: a branch pushed since shows.
	h.until("the fetched branch", func() bool {
		return slices.ContainsFunc(tb.pick.rows, func(r pickRow) bool { return r.ref == "refs/remotes/origin/pushed-since" })
	})
	if len(tb.pick.rows) != 6 { // 3 local, the heading, 2 remote: origin/main is main's
		t.Fatalf("rows %v", tb.pick.rows)
	}
	// The highlight follows the pointer on the frame it moves: the row
	// under it is drawn in the accent color.
	search, _ := h.tt.Find("Search Branches")
	for _, i := range []int{1, 0} {
		x, y := search.X+40, search.Y+search.H+spaceS+float32(i*rowHeight)+rowHeight/2
		h.tt.Move(x, y) // draws the frame
		if px := h.tt.Image().RGBAAt(int(search.X)+4, int(y)); !isAccent(px) {
			t.Fatalf("pointer on row %d, row drawn %v", i, px)
		}
	}
	h.tt.Type("bet")
	h.until("the filter", shown("refs/heads/feature/beta"))
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.until("on beta", func() bool { return tb.s.State.Head.Target == "refs/heads/feature/beta" })
	if tb.pick.open {
		t.Fatal("the picker stayed open")
	}

	h.click("Branch feature/beta")
	h.tt.Type("alpha")
	h.until("alpha", shown("refs/heads/feature/alpha"))
	r0, _ := h.tt.Find("Search Branches")
	h.tt.ClickAt(r0.X+40, r0.Y+r0.H+spaceS+rowHeight/2)
	h.tt.Frame()
	h.until("on alpha", func() bool { return tb.s.State.Head.Target == "refs/heads/feature/alpha" })

	// A remote branch checks out as a local branch of its name.
	h.click("Branch feature/alpha")
	h.tt.Type("remote-only")
	h.until("the remote", shown("", "refs/remotes/origin/remote-only"))
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.until("on remote-only", func() bool { return tb.s.State.Head.Target == "refs/heads/remote-only" })

	// The palette opens it too.
	h.app.palette.open = true
	h.tt.Frame()
	h.tt.Type("switch branch")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if !tb.pick.open {
		t.Fatal("the palette did not open the picker")
	}
}
