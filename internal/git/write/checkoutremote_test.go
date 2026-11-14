package write

import (
	"strings"
	"testing"
)

// A remote branch checked out: as a new branch tracking it; refused over
// a local branch of the same name unless reset, which is backed up and
// undone.
func TestCheckoutRemote(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	one := h.r.Commit("one")
	h.r.Write("a", "2")
	two := h.r.Commit("two")
	h.r.Git("remote", "add", "origin", "https://example.invalid/r.git")
	h.r.Git("update-ref", "refs/remotes/origin/fresh", two)
	h.r.Git("update-ref", "refs/remotes/origin/main", one)
	upstream := func(b string) string {
		out, _ := h.r.Run(nil, "rev-parse", "--symbolic-full-name", b+"@{upstream}")
		return strings.TrimSpace(out)
	}

	op, err := CheckoutRemote(h.w, "refs/remotes/origin/fresh", "fresh", "", false, CarryChanges, h.seen())
	h.do(op, err, true)
	if head := strings.TrimSpace(h.r.Git("symbolic-ref", "HEAD")); head != "refs/heads/fresh" || h.rev("fresh") != two || upstream("fresh") != "refs/remotes/origin/fresh" {
		t.Fatalf("HEAD %s at %s, upstream %q", head, h.rev("fresh"), upstream("fresh"))
	}

	// main exists, at two: git refuses to make it again.
	op, err = CheckoutRemote(h.w, "refs/remotes/origin/main", "main", "", false, CarryChanges, h.seen())
	h.do(op, err, false)
	// Reset to origin/main: main's two is backed up, and undo puts it
	// and HEAD back.
	op, err = CheckoutRemote(h.w, "refs/remotes/origin/main", "main", two, true, CarryChanges, h.seen())
	h.do(op, err, true)
	if h.rev("main") != one || upstream("main") != "refs/remotes/origin/main" || strings.TrimSpace(h.r.Git("symbolic-ref", "HEAD")) != "refs/heads/main" {
		t.Fatalf("main at %s, upstream %q", h.rev("main"), upstream("main"))
	}
	if !strings.Contains(h.r.Git("for-each-ref", "--format=%(objectname)", "refs/ramus/"), two) {
		t.Fatal("main's old value not backed up")
	}
	h.undo()
	if h.rev("main") != two || strings.TrimSpace(h.r.Git("symbolic-ref", "HEAD")) != "refs/heads/fresh" {
		t.Fatalf("after undo main at %s, HEAD %s", h.rev("main"), h.r.Git("symbolic-ref", "HEAD"))
	}

	for _, bad := range [][2]string{{"refs/heads/main", "x"}, {"refs/remotes/origin/main", "-x"}} {
		if _, err := CheckoutRemote(h.w, bad[0], bad[1], "", false, CarryChanges, h.seen()); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
