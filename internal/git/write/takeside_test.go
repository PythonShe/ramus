package write

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// Saving a merge whose file cannot be written, once the snapshot is
// taken, here in a folder that refuses new files, says where the
// conflicted file is kept.
func TestResolveFailingToWriteSaysWhereTheFileIsKept(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a folder's write permission does not refuse here")
	}
	h := newHarness(t)
	h.r.Write("sub/a", "base\n")
	h.r.Commit("base")
	h.r.Git("checkout", "-q", "-b", "topic")
	h.r.Write("sub/a", "theirs\n")
	h.r.Commit("theirs")
	h.r.Git("checkout", "-q", "main")
	h.r.Write("sub/a", "ours\n")
	h.r.Commit("ours")
	h.r.Run(nil, "merge", "topic")
	conflicted := h.read("sub/a")
	seen, err := Observe(h.w.WorkTree, []string{"sub/a"})
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(h.r.Dir, "sub")
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o755) })
	op, err := Resolve(h.w, "sub/a", []byte("merged\n"), seen[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = wait(t, h.q.Submit(op))
	var ke *KeptError
	if !errors.As(err, &ke) || ke.Snapshot.Commit == "" {
		t.Fatalf("err %v", err)
	}
	if got := h.r.Git("show", ke.Snapshot.Commit+":sub/a"); got != conflicted {
		t.Fatalf("kept %q, want %q", got, conflicted)
	}
}
