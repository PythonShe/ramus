package write

import (
	"context"
	"os"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
)

// Reword, drop and fixup rewrite an older commit and move the branches
// after it, through git history and through the rebase it falls back to;
// undo puts every branch back.
func TestRewrite(t *testing.T) {
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	for _, v := range []gitexe.Version{g.Version, {Major: 2, Minor: 40, Raw: "2.40.0"}} {
		t.Run(v.Raw, func(t *testing.T) { testRewrite(t, v) })
	}
}

func testRewrite(t *testing.T, v gitexe.Version) {
	h := newHarness(t)
	for _, f := range []string{"f1", "f2", "f3"} {
		h.r.Write(f, f+"\n")
		h.r.Commit("add " + f)
	}
	h.r.Git("branch", "side", "HEAD~1")
	h.r.Write("f3", "local change\n") // kept through every rewrite
	rw := func(rev string) Rewrite {
		parent, _ := h.r.Run(nil, "rev-parse", "-q", "--verify", rev+"~1")
		return Rewrite{Commit: h.rev(rev), Parent: strings.TrimSpace(parent), Git: v, Branches: []string{"refs/heads/side"}}
	}
	before := map[string]string{"main": h.rev("main"), "side": h.rev("side")}
	undone := func(what string) {
		t.Helper()
		h.undo()
		for b, id := range before {
			if h.rev(b) != id {
				t.Fatalf("%s: %s not put back", what, b)
			}
		}
		if h.read("f3") != "local change\n" {
			t.Fatalf("%s: the local change is gone", what)
		}
	}
	subject := func(rev string) string { return strings.TrimSpace(h.r.Git("log", "-1", "--format=%s", rev)) }

	op, err := Reword(h.w, rw("main~1"), "two\n\n# a line kept\n", h.seen())
	h.do(op, err, true)
	if subject("main~1") != "two" || h.rev("side") != h.rev("main~1") {
		t.Fatalf("reword: %q, side %s main~1 %s", subject("main~1"), h.rev("side"), h.rev("main~1"))
	}
	if body := h.r.Git("log", "-1", "--format=%B", "main~1"); !strings.Contains(body, "# a line kept") {
		t.Fatalf("reword lost a line: %q", body)
	}
	if h.read("f3") != "local change\n" {
		t.Fatal("reword: the local change is gone")
	}
	undone("reword")

	op, err = DropCommit(h.w, rw("main~1"), h.seen())
	h.do(op, err, true)
	if subject("main~1") != "add f1" || h.read("f2") != "" {
		t.Fatalf("drop: main~1 %q, f2 %q", subject("main~1"), h.read("f2"))
	}
	undone("drop")

	h.r.Write("f1", "f1 fixed\n")
	h.r.Git("add", "f1")
	op, err = FixupInto(h.w, rw("main~2"), "", h.seen())
	h.do(op, err, true)
	if got := h.r.Git("show", "main~2:f1"); got != "f1 fixed\n" || subject("main~2") != "add f1" {
		t.Fatalf("fixup: f1 %q, subject %q", got, subject("main~2"))
	}
	if st := h.status(); st != "M f3" {
		t.Fatalf("fixup left %q", st)
	}
	undone("fixup")

	h.r.Git("restore", "--staged", "f1")
	h.r.Write("f1", "f1 squashed\n")
	h.r.Git("add", "f1")
	op, err = FixupInto(h.w, rw("main~2"), "one, squashed", h.seen())
	if !v.Has(gitexe.HistoryFixup) {
		if err != ErrNeedsNewerGit {
			t.Fatalf("squash on %s: %v", v.Raw, err)
		}
		return
	}
	h.do(op, err, true)
	if subject("main~2") != "one, squashed" || h.r.Git("show", "main~2:f1") != "f1 squashed\n" {
		t.Fatalf("squash: %q", subject("main~2"))
	}
}
