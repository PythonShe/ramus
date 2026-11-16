package write

import (
	"strings"
	"testing"
)

// A switch git refused over local changes goes through stashing them,
// and a pull takes them back after it.
func TestStashingTakesWhatGitRefused(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	h.r.Git("switch", "-q", "-c", "topic")
	h.r.Write("a", "topic")
	h.r.Commit("two")
	h.r.Git("switch", "-q", "main")
	h.r.Write("a", "mine")

	op := Switch(h.w, "refs/heads/topic", h.seen())
	if _, err := wait(t, h.q.Submit(op)); err == nil || !strings.Contains(err.Error(), "would be overwritten") {
		t.Fatalf("switch over a change: %v", err)
	}
	again, ok := Stashing(op)
	if !ok {
		t.Fatal("a switch cannot stash")
	}
	if _, err := wait(t, h.q.Submit(again)); err != nil {
		t.Fatal(err)
	}
	if b := strings.TrimSpace(h.r.Git("branch", "--show-current")); b != "topic" {
		t.Fatalf("on %s", b)
	}
	if s := h.r.Git("stash", "list", "--format=%gs"); !strings.Contains(s, "Ramus: before Switch to topic") {
		t.Fatalf("stashes %q", s)
	}
	if _, ok := Stashing(again); ok {
		t.Fatal("stashed twice")
	}

	pull := Op{Name: "Pull", Cmd: h.w.cmd("pull", "--no-rebase")}
	p, ok := Stashing(pull)
	if !ok || strings.Join(p.Cmd.Args, " ") != "pull --autostash --no-rebase" || p.Pre != nil {
		t.Fatalf("pull stashing: %v %+v", ok, p.Cmd.Args)
	}
	if _, ok := Stashing(Op{Name: "Commit", Cmd: h.w.cmd("commit")}); ok {
		t.Fatal("a commit made to stash")
	}
}
