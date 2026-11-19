package write

import (
	"errors"
	"testing"
)

// Taking a side that fails once the side's file is written, here on a
// clean filter that refuses, says where the conflicted file is kept, as
// any other write that kept a snapshot does.
func TestTakeSideFailingSaysWhereTheFileIsKept(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "base\n")
	h.r.Commit("base")
	h.r.Git("checkout", "-q", "-b", "topic")
	h.r.Write("a", "theirs\n")
	h.r.Commit("theirs")
	h.r.Git("checkout", "-q", "main")
	h.r.Write("a", "ours\n")
	h.r.Commit("ours")
	h.r.Run(nil, "merge", "topic")
	h.r.Write("a", "edited by hand\n")
	h.r.Write(".git/info/attributes", "a filter=refuse\n")
	// It refuses once the side was checked out, after the snapshot.
	h.r.Git("config", "filter.refuse.smudge", "touch .git/took; cat")
	h.r.Git("config", "filter.refuse.clean", "test -e .git/took && exit 1; cat")
	h.r.Git("config", "filter.refuse.required", "true")
	op, err := TakeSide(h.w, []string{"a"}, TheirSide)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wait(t, h.q.Submit(op))
	var ke *KeptError
	if !errors.As(err, &ke) || ke.Snapshot.Commit == "" {
		t.Fatalf("err %v", err)
	}
	if got := h.r.Git("show", ke.Snapshot.Commit+":a"); got != "edited by hand\n" {
		t.Fatalf("kept %q", got)
	}
}
