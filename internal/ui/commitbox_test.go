package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A hook that fails, printing a lot, leaves the box its size: its last
// lines show, the rest is in the log, and the error bar does not say it
// again. Committing without hooks goes through; Skip hooks, ticked for a
// commit, is not left ticked after it.
func TestCommitHookFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hook is a shell script")
	}
	h := newHarness(t)
	r := h.repoWith()
	var hook strings.Builder
	hook.WriteString("#!/bin/sh\necho 'lint: running checks'\n")
	for i := range 40 {
		hook.WriteString("echo 'x.go:" + string(rune('a'+i%26)) + ": something is wrong' >&2\n")
	}
	hook.WriteString("echo 'lint: 40 problems' >&2\nexit 1\n")
	path := filepath.Join(r.GitDir(), "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(hook.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	r.Write("a.txt", "a")
	r.Git("add", "a.txt")
	h.until("staged", h.shows("Staged (1)"))
	tb := h.app.current()
	cs := &tb.commit
	cs.subject = "Hooked"
	h.until("ready", func() bool { return commitReady(tb) })
	h.click("Commit to main")
	h.until("the failure", func() bool { return !cs.busy && cs.err != "" })
	h.until("the error bar told", func() bool { return cs.errChecked })
	h.tt.Frame()
	if !h.tt.HasText("Commit Without Hooks") || !h.tt.HasText("Show in Log") || failedWrite(tb) != nil {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	if text, _ := lastLines(cs.err, errLines); !strings.HasSuffix(text, "lint: 40 problems") || strings.Count(text, "\n") != errLines-1 {
		t.Fatalf("shown %q", text)
	}
	if !h.tt.HasText("a.txt") {
		t.Fatal("the staged file is no longer shown")
	}
	h.click("Commit Without Hooks")
	h.until("the commit", func() bool { return strings.TrimSpace(r.Git("log", "-1", "--format=%s")) == "Hooked" })
	h.until("the editor cleared", func() bool { return !cs.busy && cs.subject == "" && cs.err == "" })

	r.Write("b.txt", "b")
	r.Git("add", "b.txt")
	h.until("staged", h.shows("Staged (1)"))
	cs.subject, cs.noVerify = "Skipped", true
	h.until("ready", func() bool { return commitReady(tb) })
	h.click("Commit to main")
	h.until("the commit", func() bool { return strings.TrimSpace(r.Git("log", "-1", "--format=%s")) == "Skipped" })
	h.until("the editor cleared", func() bool { return !cs.busy && cs.subject == "" })
	if cs.noVerify {
		t.Fatal("Skip hooks is still ticked")
	}
}

// A commit made says so, with Undo, which takes it back and puts its
// message back in the editor. The box keeps its setup, and its Recent
// menu, while the setup is read again.
func TestCommitUndo(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	r.Write("a.txt", "a")
	r.Git("add", "a.txt")
	h.until("staged", h.shows("Staged (1)"))
	tb := h.app.current()
	cs := &tb.commit
	before := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	cs.subject, cs.body = "Add a", "Because."
	h.until("ready", func() bool { return commitReady(tb) })
	h.click("Commit to main")
	h.until("the toast", h.shows("Committed “Add a”"))
	if cs.setup == nil || !h.tt.HasText("Recent") {
		t.Fatal("the setup went while it is read again")
	}
	h.click("Undo")
	h.until("the undo", func() bool { return strings.TrimSpace(r.Git("rev-parse", "HEAD")) == before })
	if cs.subject != "Add a" || cs.body != "Because." {
		t.Fatalf("editor %q %q", cs.subject, cs.body)
	}
	h.until("staged again", h.shows("Staged (1)"))
}

// The commit button says why it is disabled; a recent message chosen
// over one written asks first.
func TestCommitWhyNotAndRecent(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	cs := &tb.commit
	h.until("the reason", func() bool { _, why := commitWhyNot(tb); return why == "Stage changes to commit" })
	r.Write("a.txt", "a")
	r.Git("add", "a.txt")
	h.until("staged", h.shows("Staged (1)"))
	if _, why := commitWhyNot(tb); why != "Write a summary to commit" {
		t.Fatalf("why %q", why)
	}

	h.until("recent", func() bool { return cs.setup != nil && len(cs.setup.Recent) > 0 })
	cs.subject = "Written"
	h.tt.Frame()
	h.click("Recent")
	if err := h.tt.ChooseMenuItem("one"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", h.shows("Replace the message?"))
	if cs.subject != "Written" {
		t.Fatalf("replaced before asking: %q", cs.subject)
	}
	h.click("Replace")
	if cs.subject != "one" {
		t.Fatalf("subject %q", cs.subject)
	}
}

// Amending a commit already on the upstream says so, and pushes with a
// force, after asking; one not pushed yet is amended and pushed.
func TestAmendPushed(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("config", "receive.denyCurrentBranch", "ignore")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("reset", "-q", "--hard", "origin/main")
	r.Git("branch", "-q", "--set-upstream-to", "origin/main")
	r.Write("a.txt", "a\n")
	r.Commit("add a")
	r.Git("push", "-q", "origin", "main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	cs := &tb.commit
	h.until("set up", func() bool { return cs.setup != nil })
	cs.amend = true
	h.until("the pushed commit", h.shows("Amend Pushed Commit"))
	h.until("its message", func() bool { return cs.subject == "add a" })
	cs.subject = "add a, amended"
	h.click("Amend and Force Push…")
	h.until("the question", h.shows("Overwrite origin/main?"))
	h.click("Amend and Force Push")
	h.until("the force push", func() bool {
		out, _ := up.Run(nil, "log", "-1", "--format=%s", "main")
		return strings.TrimSpace(out) == "add a, amended"
	})
	h.idle(tb, "the push done")
	if h.tt.HasText("was rewritten after it was pushed") {
		t.Fatal("a force push offered after one")
	}

	r.Write("b.txt", "b\n")
	r.Commit("add b")
	h.until("set up again", func() bool { return !cs.busy && cs.setup != nil && !cs.loading })
	cs.amend = true
	h.until("the commit not pushed", h.shows("Amend and Push"))
	if h.tt.HasText("Amend Pushed Commit") {
		t.Fatal("a commit not pushed told as pushed")
	}
}

// With Amend ticked, the commit box and the sync button both read the
// track as the refs change; neither is left waiting on a read the other
// replaced.
func TestAmendTrackShared(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	up.Git("config", "receive.denyCurrentBranch", "ignore")
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("reset", "-q", "--hard", "origin/main")
	r.Git("branch", "-q", "--set-upstream-to", "origin/main")
	r.Write("a.txt", "a\n")
	r.Commit("add a")
	r.Git("push", "-q", "origin", "main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	cs := &tb.commit
	h.until("set up", func() bool { return cs.setup != nil })
	cs.amend = true
	h.until("the pushed commit", h.shows("Amend Pushed Commit"))
	r.Write("b.txt", "b\n")
	r.Commit("add b")
	h.until("the sync button ahead", h.shows("Push 1 commit to origin/main"))
	h.until("the commit box ahead", func() bool {
		return !cs.trackLoading && cs.trackRead == "refs/heads/main" && cs.track.Ahead == 1
	})
}

// While a merge waits for its commit, the box offers git's message and
// commits the merge once conflicts are resolved; while a rebase waits on
// its continue it is not there, but where it stopped to edit a commit.
func TestCommitDuringOperations(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "base\n")
	r.Commit("base")
	r.Git("switch", "-q", "-c", "other")
	r.Write("a.txt", "theirs\n")
	r.Commit("theirs")
	r.Git("switch", "-q", "main")
	r.Write("a.txt", "ours\n")
	r.Commit("ours")
	if _, err := r.Run(nil, "merge", "-q", "other"); err == nil {
		t.Fatal("the merge did not stop")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the merge", h.shows("Commit Merge"))
	tb := h.app.current()
	cs := &tb.commit
	h.until("git's message", func() bool { return cs.subject == "Merge branch 'other'" })
	if cs.body != "" {
		t.Fatalf("body %q", cs.body)
	}
	h.until("the reason", func() bool { _, why := commitWhyNot(tb); return why == "Resolve the conflicts to commit" })
	r.Write("a.txt", "both\n")
	r.Git("add", "a.txt")
	h.until("resolved", func() bool { return commitReady(tb) })
	h.click("Commit Merge")
	h.until("the merge commit", func() bool {
		return strings.TrimSpace(r.Git("log", "-1", "--format=%P")) != "" && len(strings.Fields(r.Git("log", "-1", "--format=%P"))) == 2
	})
	if msg := strings.TrimSpace(r.Git("log", "-1", "--format=%B")); msg != "Merge branch 'other'" {
		t.Fatalf("message %q", msg)
	}

	// A rebase stopped on a conflict: its continue commits.
	h.idle(tb, "the merge done")
	r.Git("switch", "-q", "-c", "side", "HEAD~1")
	r.Write("a.txt", "side\n")
	r.Commit("side")
	if _, err := r.Run(nil, "rebase", "-q", "main"); err == nil {
		t.Fatal("the rebase did not stop")
	}
	h.until("the rebase", func() bool { return tb.s.State.Operation == "rebase" })
	h.until("the box gone", func() bool { return !h.tt.HasText("Commit and Push") })
	r.Git("rebase", "--abort")

	// A rebase stopped to edit a commit: the box amends it.
	r.Env = []string{"GIT_SEQUENCE_EDITOR=sed -i.bak -e 1s/^pick/edit/"}
	r.Git("rebase", "-q", "-i", "HEAD~1")
	r.Env = nil
	h.until("the edit stop", func() bool { return tb.s.State.Operation == "rebase" && cs.prepRead })
	h.until("the box", h.shows("Commit and Push"))
}
