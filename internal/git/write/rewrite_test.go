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

// An edit of history replays a whole todo: commits reordered, reworded,
// squashed with a message or joined, fixed up and dropped, a branch left
// where its step says; undo puts it all back.
func TestEditHistory(t *testing.T) {
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	h := newHarness(t)
	for i := 1; i <= 6; i++ {
		f := "f" + string(rune('0'+i))
		h.r.Write(f, f+"\n")
		h.r.Commit("add " + f)
	}
	h.r.Git("branch", "mid", "HEAD~2")
	h.r.Write("f6", "local change\n")
	id := func(n int) string { return h.rev("main~" + string(rune('0'+6-n))) } // commit n, 1 to 6
	rw := Rewrite{Commit: id(2), Parent: id(1), Git: g.Version, Branches: []string{"refs/heads/mid"}}
	before := map[string]string{"main": h.rev("main"), "mid": h.rev("mid")}
	steps := []Step{
		{Action: DoPick, Commit: id(3)},
		{Action: DoReword, Commit: id(2), Message: "two, it's \\n \"quoted\"\n\n# a line kept\n"},
		{Action: DoSquash, Commit: id(4), Message: "four into two"},
		{Action: DoDrop, Commit: id(5), Refs: []string{"refs/heads/mid"}},
		{Action: DoPick, Commit: id(6)},
	}
	op, err := EditHistory(h.w, rw, steps, h.seen())
	h.do(op, err, true)
	log := strings.TrimSpace(h.r.Git("log", "--format=%s", "main"))
	if log != "add f6\nfour into two\nadd f3\nadd f1" {
		t.Fatalf("log:\n%s", log)
	}
	if h.rev("mid") != h.rev("main~1") {
		t.Fatalf("mid at %s, not at the squash", h.rev("mid"))
	}
	if h.read("f5") != "" || h.r.Git("show", "main~1:f4") != "f4\n" || h.read("f6") != "local change\n" {
		t.Fatal("the files are not as the steps make them")
	}
	h.undo()
	for b, id := range before {
		if h.rev(b) != id {
			t.Fatalf("%s not put back", b)
		}
	}

	// A reworded message keeps every byte, comment lines included.
	steps = []Step{{Action: DoReword, Commit: id(2), Message: "two, it's \\n \"quoted\" $HOME `x`\n\n# a line kept\n"}}
	for n := 3; n <= 6; n++ {
		steps = append(steps, Step{Action: DoFixup, Commit: id(n)})
	}
	op, err = EditHistory(h.w, rw, steps, h.seen())
	h.do(op, err, true)
	if body := h.r.Git("log", "-1", "--format=%B", "main"); body != "two, it's \\n \"quoted\" $HOME `x`\n\n# a line kept\n\n" {
		t.Fatalf("message %q", body)
	}
	h.undo()

	// Squash with no message joins them, as git does.
	op, err = EditHistory(h.w, rw, []Step{{Action: DoPick, Commit: id(2)}, {Action: DoSquash, Commit: id(3)},
		{Action: DoPick, Commit: id(4)}, {Action: DoPick, Commit: id(5)}, {Action: DoPick, Commit: id(6)}}, h.seen())
	h.do(op, err, true)
	if body := h.r.Git("log", "-1", "--format=%B", "main~3"); body != "add f2\n\nadd f3\n\n" {
		t.Fatalf("joined message %q", body)
	}
	h.undo()

	for _, bad := range [][]Step{
		{{Action: DoFixup, Commit: id(2)}},
		{{Action: DoDrop, Commit: id(2)}, {Action: DoSquash, Commit: id(3)}},
		{{Action: DoReword, Commit: id(2), Message: " \n"}},
		{{Action: DoPick, Commit: id(2)}, {Action: DoPick, Commit: id(2)}},
		{{Action: DoPick, Commit: "--root"}},
		{{Action: DoPick, Commit: id(2), Refs: []string{"refs/tags/x"}}},
	} {
		if _, err := EditHistory(h.w, rw, bad, h.seen()); err == nil {
			t.Errorf("%+v taken", bad)
		}
	}
}
