package ui

import (
	"errors"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/write"
	"ramus/locales"
)

// pushGuardRepo is a repository open in a tab with main one commit
// ahead of origin/main, or with no upstream when publish is set, and a
// branch other, ahead of its own upstream origin/other: a push that ran
// on other would send it there. It returns the tab and the remote.
func pushGuardRepo(t *testing.T, h *harness, publish bool) (*tab, *gittest.Repo) {
	t.Helper()
	origin := gittest.Init(t, "--bare")
	r := h.repoWith()
	r.Git("remote", "add", "origin", origin.Dir)
	r.Write("a.txt", "a\n")
	r.Commit("add a")
	r.Git("switch", "-q", "-c", "other")
	r.Git("push", "-q", "-u", "origin", "other")
	r.Write("o.txt", "o\n")
	r.Commit("other work")
	r.Git("switch", "-q", "main")
	if !publish {
		r.Git("push", "-q", "-u", "origin", "main")
		r.Write("b.txt", "b\n")
		r.Commit("add b")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	return h.app.current(), origin
}

// queueSwitch queues a switch to other, which runs before what is asked
// after it; the tab still shows main until the updates are taken.
func queueSwitch(t *testing.T, tb *tab) {
	t.Helper()
	if _, err := tb.s.Submit(write.Switch(tb.s.Where(), "refs/heads/other", tb.s.Seen())); err != nil {
		t.Fatal(err)
	}
}

// pushRefused checks that the push asked for on main, run after the
// switch, sent nothing and said why.
func pushRefused(t *testing.T, h *harness, tb *tab, origin *gittest.Repo, published bool) {
	t.Helper()
	h.idle(tb, "the switch and the push")
	if tb.s.State.Head.Target != "refs/heads/other" {
		t.Fatalf("HEAD on %s", tb.s.State.Head.Target)
	}
	l := tb.s.Log
	var stale *write.StaleError
	if e := l[len(l)-1]; !strings.Contains(e.Name, "Push") || !errors.As(e.Err, &stale) {
		t.Fatalf("last write %q ended with %v", e.Name, e.Err)
	}
	if out, _ := origin.Run(nil, "log", "-1", "--format=%s", "other"); strings.TrimSpace(out) != "add a" {
		t.Fatalf("other pushed: %q", out)
	}
	want := "add a"
	if published {
		want = ""
	}
	if out, _ := origin.Run(nil, "log", "-1", "--format=%s", "main"); strings.TrimSpace(out) != want {
		t.Fatalf("main on the remote: %q", out)
	}
	h.until("the reason", h.shows(errText(l[len(l)-1].Err)))
}

// A push asked for from the palette, the sync button, or its Publish,
// with HEAD on main and a switch to other queued before it, refuses once
// HEAD is on other, as commit and push does, rather than push other.
func TestPushAskedOnABranchSwitchedFrom(t *testing.T) {
	t.Run("palette", func(t *testing.T) {
		h := newHarness(t)
		tb, origin := pushGuardRepo(t, h, false)
		queueSwitch(t, tb)
		h.app.runCommand("push")
		pushRefused(t, h, tb, origin, false)
	})
	t.Run("sync button", func(t *testing.T) {
		h := newHarness(t)
		tb, origin := pushGuardRepo(t, h, false)
		label := locales.SyncPushLabel(1, "origin/main")
		h.until("the push", h.labelled(label))
		queueSwitch(t, tb)
		h.click(label)
		pushRefused(t, h, tb, origin, false)
	})
	t.Run("publish", func(t *testing.T) {
		h := newHarness(t)
		tb, origin := pushGuardRepo(t, h, true)
		h.until("the publish", h.labelled(locales.SyncPublish()))
		queueSwitch(t, tb)
		h.click(locales.SyncPublish())
		pushRefused(t, h, tb, origin, true)
	})
}
