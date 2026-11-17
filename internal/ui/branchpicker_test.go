package ui

import (
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// The picker lists the pinned branches first, then the others by their
// last commit, the newest first, each with how long ago in a few letters.
func TestBranchPickerByDate(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "old")
	r.Write("b", "2")
	r.CommitAt("two", time.Now().Add(-2*time.Hour).Unix())
	r.Git("branch", "newer")
	r.Git("branch", "alpha", "HEAD~1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	order := func(want ...string) func() bool {
		return func() bool {
			var got []string
			for _, r := range tb.pick.rows {
				got = append(got, r.ref)
			}
			return slices.Equal(got, want)
		}
	}
	h.click("Branch main")
	h.until("by date", order("refs/heads/main", "refs/heads/newer", "refs/heads/alpha", "refs/heads/old"))
	h.until("the date", func() bool { return hasExact(h, "2h") })
	tb.pick.open = false
	h.tt.Frame()

	h.menu("old", "Pin to Top")
	h.click("Branch main")
	h.until("pinned first", order("refs/heads/old", "refs/heads/main", "refs/heads/newer", "refs/heads/alpha"))
}

// A remote branch is one row with the local branch of its name only when
// that tracks it: one named whole in the query shows, and is not offered
// to create, and another remote's of that name shows too.
func TestBranchPickerRemoteOfLocalName(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("branch", "topic")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("remote", "add", "upstream", up.Dir)
	r.Git("fetch", "-q", "--all")
	r.Git("branch", "-q", "--track", "topic", "origin/topic")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("Branch main")
	h.tt.Type("origin/topic")
	h.until("the remote branch", func() bool {
		return slices.ContainsFunc(tb.pick.rows, func(r pickRow) bool { return r.ref == "refs/remotes/origin/topic" })
	})
	if h.tt.HasText("Create Branch origin/topic…") {
		t.Fatal("offered to create a branch named as a remote one")
	}
	tb.pick.query = ""
	h.tt.Frame()
	h.tt.Type("topic")
	h.until("the local branch and the other remote's", func() bool {
		var got []string
		for _, r := range tb.pick.rows {
			got = append(got, r.ref)
		}
		return slices.Equal(got, []string{"refs/heads/topic", "", "refs/remotes/upstream/topic"})
	})
}

// The branch button in the tab bar names the branch; its panel fetches,
// filters the branches by what is typed, and a click or Enter switches.
func TestBranchPicker(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("branch", "remote-only")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("branch", "-q", "--set-upstream-to", "origin/main")
	r.Git("branch", "feature/alpha")
	r.Git("branch", "feature/beta")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
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
	if len(tb.pick.rows) != 6 { // 3 local, the heading, 2 remote: origin/main is main's upstream
		t.Fatalf("rows %v", tb.pick.rows)
	}
	// The highlight follows the pointer on the frame it moves: the row
	// under it is drawn in the accent color.
	search, _ := h.tt.Find("Search branches")
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
	r0, _ := h.tt.Find("Search branches")
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

// What a branch's menu in the picker asks, over the Changes page, shows:
// deleting a branch, and switching over local changes.
func TestBranchPickerAsks(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "other")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.setSidebar(false)
	h.click("Branch main")
	tb := h.app.current()
	h.until("the branch and its state", func() bool { _, ok := tb.pick.info["refs/heads/other"]; return ok && hasExact(h, "other") })
	if err := h.tt.RightClick("other"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if err := h.tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", h.shows("Delete branch other at "))
	h.click("Cancel")

	r.Write("README", "changed")
	h.until("the change", func() bool { st := tb.s.State.Status; return st != nil && st.Len() > 0 })
	h.click("Branch main")
	h.tt.Type("other")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.until("the switch question", h.shows("Switch to other with local changes?"))
}

// A fetch landing while the picker is open shows in its counts.
func TestBranchPickerCountsAfterFetch(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("branch", "-q", "--set-upstream-to", "origin/main")
	r.Git("reset", "-q", "--hard", "origin/main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.setSidebar(false)
	tb := h.app.current()
	h.click("Branch main")
	h.until("main's state", func() bool { _, ok := tb.pick.info["refs/heads/main"]; return ok })
	h.until("the picker's fetch", func() bool { return !tb.s.Fetching() })
	up.Write("c", "3")
	up.Commit("upstream moved")
	r.Git("fetch", "-q", "origin")
	h.until("one behind", func() bool { return tb.pick.open && tb.pick.info["refs/heads/main"].Behind == 1 })
}
