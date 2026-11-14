package write

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"ramus/internal/git/run"
)

// A switch that stashed the changes and then failed puts them back,
// staged as they were, popping the stash it made though another was
// pushed over it; a pop that fails names the stash they are kept in;
// a switch that happened before failing leaves them stashed, as asked.
func TestSwitchStashingPutsChangesBackOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing commands are shell aliases")
	}
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Write("b", "1")
	h.r.Commit("one")
	h.r.Git("branch", "topic")
	h.r.Write("b", "the user's")
	h.r.Git("stash", "push", "-q", "-m", "the user's own") // must not be popped
	h.r.Write("a", "staged")
	h.r.Git("add", "a")
	h.r.Write("b", "changed")
	h.r.Write("new", "untracked")
	want := h.status()
	stashes := func() string { return strings.TrimSpace(h.r.Git("stash", "list", "--format=%gs")) }
	ours := stashes()

	// A branch that is not there: git switch fails.
	_, err := wait(t, h.q.Submit(SwitchWith(h.w, "refs/heads/gone", StashChanges, h.seen())))
	if err == nil {
		t.Fatal("switched to a branch that is not there")
	}
	var kept *StashKeptError
	if errors.As(err, &kept) || h.status() != want || stashes() != ours {
		t.Fatalf("after a failed switch: %v; status %q, want %q; stashes %q", err, h.status(), want, stashes())
	}

	// The failing write pushes a stash over the one Pre made, or leaves
	// the work tree in the way of the pop, or switches and fails.
	failing := func(script string) error {
		op := SwitchWith(h.w, "refs/heads/topic", StashChanges, h.seen())
		op.Cmd.Config = []string{"alias.fail=!sh -c '" + script + "; exit 1'"}
		op.Cmd.Args = []string{"fail"}
		_, err := wait(t, h.q.Submit(op))
		if err == nil {
			t.Fatal("the failing write did not fail")
		}
		return err
	}
	err = failing(`echo other >other && git stash push -q -u -m pushed-over`)
	if errors.As(err, &kept) || h.status() != want || !strings.HasPrefix(stashes(), "On main: pushed-over\n") {
		t.Fatalf("with a stash pushed over it: %v; status %q, want %q; stashes %q", err, h.status(), want, stashes())
	}
	h.r.Git("stash", "drop", "-q")

	err = failing(`echo in-the-way >b`)
	if !errors.As(err, &kept) || kept.Entry != "stash@{0}" || !strings.Contains(err.Error(), "stash@{0}") {
		t.Fatalf("a pop that failed: %v", err)
	}
	var re *run.Error
	if !errors.As(err, &re) {
		t.Fatalf("the switch's own error is lost: %v", err)
	}
	if s := stashes(); !strings.HasPrefix(s, "On main: Ramus: before switching to topic\n") {
		t.Fatalf("stashes %q", s)
	}
	// git put the untracked file back before refusing; the stash keeps
	// it all the same.
	h.r.Git("checkout", "-q", "--", "b")
	h.r.Git("clean", "-q", "-f")
	h.r.Git("stash", "pop", "-q", "--index")
	if h.status() != want {
		t.Fatalf("status %q, want %q", h.status(), want)
	}

	err = failing(`git switch -q topic`)
	if errors.As(err, &kept) || h.seen().Branch != "refs/heads/topic" || h.status() != "" ||
		!strings.HasPrefix(stashes(), "On main: Ramus: before switching to topic\n") {
		t.Fatalf("a switch that happened and failed: %v; on %s; status %q; stashes %q", err, h.seen().Branch, h.status(), stashes())
	}
}
