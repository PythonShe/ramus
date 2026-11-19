package write

import (
	"context"
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

	// Pushed over it once it was found: the one dropped was the other.
	h.r.Git("checkout", "-q", "--", "a")
	mine = stash("mine again\n", "mine again")
	third := stash("third\n", "third")
	res, err := h.q.r.Run(context.Background(), &run.Cmd{GitDir: h.w.GitDir, WorkTree: h.r.Dir, Untranslated: true,
		Args: []string{"stash", "drop", "stash@{0}"}})
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) (run.Result, error) {
		return h.q.r.Run(context.Background(), &run.Cmd{GitDir: h.w.GitDir, WorkTree: h.r.Dir, Untranslated: true, Args: args})
	}
	if err := h.q.putBack(context.Background(), git, mine, string(res.Stdout)); err == nil {
		t.Fatal("no error for a stash dropped in its place")
	}
	for _, id := range []string{mine, other, third} {
		if !strings.Contains(list(), id) {
			t.Fatalf("stash %s lost: %q", id, list())
		}
	}
}
