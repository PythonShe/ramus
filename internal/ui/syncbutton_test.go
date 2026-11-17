package ui

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/write"
)

// The button beside the branch picker publishes a branch with no
// upstream, pushes one ahead, and pulls one behind, saying how each went;
// one both ahead and behind shows both and pulls first.
func TestSyncButton(t *testing.T) {
	h := newHarness(t)
	origin := gittest.Init(t, "--bare")
	other := h.repoWith()
	other.Git("remote", "add", "origin", origin.Dir)
	other.Git("push", "-q", "origin", "HEAD:refs/heads/main")
	r := gittest.Init(t)
	r.Git("remote", "add", "origin", origin.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("reset", "-q", "--hard", "origin/main")
	r.Git("switch", "-q", "-c", "topic")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()

	h.until("publish", h.shows("Publish Branch"))
	h.click("Publish Branch")
	h.until("the toast of the push", h.shows("Pushed topic to origin"))
	h.until("published", func() bool {
		out, _ := origin.Run(nil, "rev-parse", "--verify", "-q", "topic")
		return strings.TrimSpace(out) != ""
	})
	h.idle(tb, "the push done")

	r.Write("mine", "x")
	r.Commit("mine")
	h.until("ahead", h.shows("Push 1 commit to origin/topic"))
	h.click("Push 1 commit to origin/topic")
	h.until("pushed", func() bool {
		a, _ := origin.Run(nil, "rev-parse", "topic")
		b, _ := r.Run(nil, "rev-parse", "HEAD")
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	})
	h.idle(tb, "the push done")

	other.Git("fetch", "-q", "origin")
	other.Git("switch", "-q", "-c", "topic", "origin/topic")
	other.Write("theirs", "x")
	theirs := other.Commit("theirs")
	other.Git("push", "-q", "origin", "topic")
	h.until("even", h.labelled("Fetch"))
	h.click("Fetch")
	h.until("the toast of the fetch", h.shows("Fetched all remotes"))
	h.until("behind", h.shows("Pull 1 commit from origin/topic"))

	r.Write("more", "x")
	r.Commit("more")
	diverged := "Pull 1 commit from origin/topic; 1 commit to push after"
	h.until("diverged", h.shows(diverged))
	h.click(diverged)
	h.until("pulled", func() bool {
		_, err := r.Run(nil, "merge-base", "--is-ancestor", theirs, "HEAD")
		return err == nil
	})
	h.until("the toast of the pull", h.shows("Pulled 1 commit into topic"))
	h.idle(tb, "the pull done")
	h.until("ahead after the pull", h.shows("Push 2 commits to origin/topic"))

	// The menu beside it pushes too.
	h.click("Fetch, Pull or Push")
	if err := h.tt.ChooseMenuItem("Push"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("pushed from the menu", func() bool {
		a, _ := origin.Run(nil, "rev-parse", "topic")
		b, _ := r.Run(nil, "rev-parse", "HEAD")
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	})
	h.until("even again", h.labelled("Fetch"))
}

// A branch whose upstream the remote deleted is offered to publish
// again, from the button and the palette, and no pull of what is gone.
func TestSyncGoneUpstream(t *testing.T) {
	h := newHarness(t)
	origin := gittest.Init(t, "--bare")
	r := h.repoWith()
	r.Git("remote", "add", "origin", origin.Dir)
	r.Git("switch", "-q", "-c", "topic")
	r.Git("push", "-q", "-u", "origin", "topic")
	origin.Git("branch", "-q", "-D", "topic")
	r.Git("fetch", "-q", "--prune", "origin")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.until("publish", h.shows("Publish Branch"))
	for _, c := range h.app.commands("") {
		if strings.HasPrefix(c.id, "pull-") {
			t.Fatalf("the palette offers %s of a gone upstream", c.id)
		}
	}
	h.click("Publish Branch")
	h.until("published", func() bool {
		out, _ := origin.Run(nil, "rev-parse", "--verify", "-q", "topic")
		return strings.TrimSpace(out) != ""
	})
	h.idle(tb, "the push done")
	h.until("even", h.labelled("Fetch"))
}

// syncOf tells the writes the sync button follows from the others.
func TestSyncOf(t *testing.T) {
	w := write.Where{GitDir: "/r/.git", WorkTree: "/r"}
	fetchAll, _ := write.Fetch(w, "", false)
	fetchOne, _ := write.Fetch(w, "upstream", true)
	pull, _ := write.Pull(w, write.PullRebase, nil, write.Seen{Branch: "refs/heads/main", Head: "x"})
	pr, _ := write.FetchPull(w, "origin", 7, "refs/pull/7/head", "main", 0)
	tag, _ := write.PushTag(w, "origin", "v1")
	for _, c := range []struct {
		op     write.Op
		kind   syncKind
		remote string
	}{
		{fetchAll, syncFetch, ""},
		{fetchOne, syncFetch, "upstream"},
		{pull, syncPull, ""},
		{write.Push(w, write.PushOptions{}), syncPush, ""},
		{write.Push(w, write.PushOptions{Remote: "origin"}), syncPush, "origin"},
		{write.Push(w, write.PushOptions{ForceWithLease: true}), syncPush, ""},
		{pr, syncNone, ""},
		{tag, syncNone, ""},
	} {
		if k, r := syncOf(c.op); k != c.kind || r != c.remote {
			t.Errorf("%q: %v %q, want %v %q", c.op.Cmd.Args, k, r, c.kind, c.remote)
		}
	}
}

// labelled reports whether the last frame shows an element showing or
// labeled exactly s.
func (h *harness) labelled(s string) func() bool {
	return func() bool { _, ok := h.tt.Find(s); return ok }
}
