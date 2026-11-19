package write

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ramus/internal/git/run"
)

// The stash a write made is taken back by its commit and dropped from
// where the list holds it, other stashes left as they are; a stash
// pushed between finding it and dropping it, and dropped in its place,
// is stored again, and the error says the stash was not dropped.
func TestPopStashTakesItsOwnStash(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1\n")
	h.r.Commit("one")
	stash := func(text, msg string) string {
		h.r.Write("a", text)
		h.r.Git("stash", "push", "-q", "-m", msg)
		return strings.TrimSpace(h.r.Git("rev-parse", "refs/stash"))
	}
	list := func() string { return h.r.Git("stash", "list", "--format=%H") }
	mine := stash("mine\n", "mine")
	other := stash("other\n", "other")
	e := Entry{Name: "Switch", stash: mine, op: Op{Cmd: run.Cmd{WorkTree: h.r.Dir}}}
	if err := h.q.popStash(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if h.read("a") != "mine\n" || list() != other+"\n" {
		t.Fatalf("a %q, stashes %q", h.read("a"), list())
	}

	// Another pushed over it in a terminal once it was found: the one git
	// dropped was the other, which is stored again.
	h.r.Git("checkout", "-q", "--", "a")
	mine = stash("mine again\n", "mine again")
	var third string
	h.q.beforeDrop = func() {
		h.r.Write("b", "third\n")
		h.r.Git("stash", "push", "-q", "--include-untracked", "-m", "third", "--", "b")
		third = strings.TrimSpace(h.r.Git("rev-parse", "refs/stash"))
	}
	e = Entry{Name: "Switch", stash: mine, op: Op{Cmd: run.Cmd{WorkTree: h.r.Dir}}}
	var se *StashKeptError
	if err := h.q.popStash(context.Background(), e); !errors.As(err, &se) {
		t.Fatalf("no error for a stash dropped in its place: %v", err)
	}
	if third == "" || h.read("a") != "mine again\n" {
		t.Fatalf("a %q, third %q", h.read("a"), third)
	}
	for _, id := range []string{other, third} {
		if !strings.Contains(list(), id) {
			t.Fatalf("stash %s lost: %q", id, list())
		}
	}
}
