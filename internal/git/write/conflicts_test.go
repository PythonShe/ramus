package write

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conflicted makes a merge of topic into main stop on a.txt and b.bin.
func conflicted(t *testing.T) *harness {
	h := newHarness(t)
	h.r.Write("a.txt", "one\n")
	h.r.Write("b.bin", "\x00base")
	h.r.Write("c.txt", "c\n")
	h.r.Commit("base")
	h.r.Git("switch", "-q", "-c", "topic")
	h.r.Write("a.txt", "theirs\n")
	h.r.Write("b.bin", "\x00theirs")
	h.r.Git("rm", "-q", "c.txt")
	h.r.Commit("topic")
	h.r.Git("switch", "-q", "main")
	h.r.Write("a.txt", "ours\n")
	h.r.Write("b.bin", "\x00ours")
	h.r.Write("c.txt", "c changed\n")
	h.r.Commit("main")
	if out, err := h.r.Run(nil, "merge", "topic"); err == nil {
		t.Fatalf("merged cleanly: %s", out)
	}
	return h
}

func TestResolveConflicts(t *testing.T) {
	h := conflicted(t)
	op, err := TakeSide(h.w, []string{"b.bin"}, TheirSide)
	h.do(op, err, true)
	if h.read("b.bin") != "\x00theirs" {
		t.Fatalf("b.bin %q", h.read("b.bin"))
	}
	op, err = RemoveConflicted(h.w, []string{"c.txt"})
	h.do(op, err, true)

	seen, err := Observe(h.w.WorkTree, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// A file changed since it was read is not overwritten.
	h.r.Write("a.txt", h.read("a.txt")+"edited\n")
	op, err = Resolve(h.w, "a.txt", []byte("merged\n"), seen[0])
	h.do(op, err, false)
	seen, _ = Observe(h.w.WorkTree, []string{"a.txt"})
	op, err = Resolve(h.w, "a.txt", []byte("merged\n"), seen[0])
	h.do(op, err, true)
	if h.read("a.txt") != "merged\n" || strings.Contains(h.r.Git("ls-files", "-u"), "a.txt") {
		t.Fatalf("a.txt %q, unmerged %q", h.read("a.txt"), h.r.Git("ls-files", "-u"))
	}
	if _, err := Resolve(h.w, "../out", nil, seen[0]); err == nil {
		// Refused when it runs, if not before.
		op, _ := Resolve(h.w, "../out", []byte("x"), FileState{Path: "../out"})
		h.do(op, nil, false)
	}

	op, err = Sequence(h.w, "merge", Continue)
	h.do(op, err, true)
	if parents := strings.Fields(h.r.Git("log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("no merge commit: %v", parents)
	}
	if _, err := Sequence(h.w, "merge", Skip); err == nil {
		t.Fatal("a merge skipped")
	}
	if _, err := Sequence(h.w, "--exec", Continue); err == nil {
		t.Fatal("an option taken as an operation")
	}
}

func TestAbortSkipAndMergeTool(t *testing.T) {
	h := conflicted(t)
	op, err := Sequence(h.w, "merge", Abort)
	h.do(op, err, true)
	if _, err := os.Stat(filepath.Join(h.r.GitDir(), "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("still merging")
	}

	// A rebase that stops can skip the commit.
	h.r.Git("switch", "-q", "topic")
	if _, err := h.r.Run(nil, "rebase", "main"); err == nil {
		t.Fatal("rebased cleanly")
	}
	op, err = Sequence(h.w, "rebase", Skip)
	h.do(op, err, true)
	if _, err := os.Stat(filepath.Join(h.r.GitDir(), "rebase-merge")); !os.IsNotExist(err) {
		t.Fatal("still rebasing")
	}

}

// The merge tool from the config writes the result and git marks it
// resolved.
func TestMergeTool(t *testing.T) {
	h := conflicted(t)
	h.r.Git("config", "mergetool.fake.cmd", `printf 'from tool\n' > "$MERGED"`)
	h.r.Git("config", "mergetool.fake.trustExitCode", "true")
	h.r.Git("config", "merge.tool", "fake")
	op, err := MergeTool(h.w, []string{"a.txt"}, false)
	h.do(op, err, true)
	if h.read("a.txt") != "from tool\n" || strings.Contains(h.r.Git("ls-files", "-u"), "a.txt") {
		t.Fatalf("a.txt %q, unmerged %q", h.read("a.txt"), h.r.Git("ls-files", "-u"))
	}
}
