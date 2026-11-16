package ui

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// The button beside the branch picker publishes a branch with no
// upstream, pushes one ahead, and pulls one behind.
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
	h.until("published", func() bool {
		out, _ := origin.Run(nil, "rev-parse", "--verify", "-q", "topic")
		return strings.TrimSpace(out) != ""
	})
	h.idle(tb, "the push done")

	r.Write("mine", "x")
	r.Commit("mine")
	h.until("ahead", h.shows("Push ↑1"))
	h.click("Push ↑1")
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
	h.until("even", h.shows("Fetch"))
	h.click("Fetch")
	h.until("behind", h.shows("Pull ↓1"))
	h.click("Pull ↓1")
	h.until("pulled", func() bool {
		_, err := r.Run(nil, "merge-base", "--is-ancestor", theirs, "HEAD")
		return err == nil
	})
}
