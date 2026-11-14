package ui

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The branch button in the tab bar names the branch; its panel filters
// the branches by what is typed, and a click or Enter switches.
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
	h.until("the branches", func() bool { return len(tb.pick.rows) == 6 }) // 3 local, the heading, 2 remote
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
