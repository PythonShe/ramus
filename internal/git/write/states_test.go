package write

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitDirState is the top of the git directory, each file with its bytes,
// but for what every write changes and undo moves itself (the index,
// objects, refs and their logs) and COMMIT_EDITMSG, the message of the
// last commit made, which git does not read back.
func (h *harness) gitDirState() map[string]string {
	h.t.Helper()
	ents, err := os.ReadDir(h.w.GitDir)
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range ents {
		switch n := e.Name(); {
		case slices.Contains([]string{"index", "objects", "refs", "logs", "packed-refs", "COMMIT_EDITMSG"}, n):
		case e.IsDir():
			out[n+"/"] = ""
		default:
			b, err := os.ReadFile(filepath.Join(h.w.GitDir, n))
			if err != nil {
				h.t.Fatal(err)
			}
			out[n] = string(b)
		}
	}
	return out
}

func diffStates(a, b map[string]string) string {
	var out []string
	names := maps.Clone(a)
	maps.Copy(names, b)
	for _, n := range slices.Sorted(maps.Keys(names)) {
		va, oka := a[n]
		vb, okb := b[n]
		if va != vb || oka != okb {
			out = append(out, fmt.Sprintf("%s: %v %q, then %v %q", n, oka, va, okb, vb))
		}
	}
	return strings.Join(out, "; ")
}

// The state files a merge, a squash merge, a cherry-pick, a revert, a
// rebase or a reset leaves in the git directory, or a commit ending a
// merge takes away, are as they were before it once it is undone, one
// there before with the bytes it had then, and as it left them once it
// is redone.
func TestUndoPutsGitsStateFilesBack(t *testing.T) {
	setup := func(t *testing.T, conflict bool) (*harness, string) {
		h := newHarness(t)
		h.r.Write("a", "1\n")
		one := h.r.Commit("one")
		h.r.Git("switch", "-q", "-c", "side")
		h.r.Write("b", "side\n")
		if conflict {
			h.r.Write("a", "side\n")
		}
		h.r.Commit("side")
		h.r.Git("switch", "-q", "main")
		h.r.Write("a", "main\n")
		h.r.Commit("main")
		// What an earlier command left, which the write is not to lose.
		h.r.Git("update-ref", "ORIG_HEAD", one)
		h.r.Write(".git/SQUASH_MSG", "an earlier squash's message\n")
		return h, one
	}
	side := func(h *harness) string { return h.rev("side") }
	for _, c := range []struct {
		name     string
		conflict bool
		ok       bool
		op       func(h *harness, one string) (Op, error)
	}{
		{"merge", false, true, func(h *harness, _ string) (Op, error) { return Merge(h.w, "side", MergeNoFF, h.seen()) }},
		{"fast-forward", false, true, func(h *harness, _ string) (Op, error) {
			h.r.Git("reset", "-q", "--hard", "HEAD~")
			return Merge(h.w, "side", MergeDefault, h.seen())
		}},
		{"squash merge", false, true, func(h *harness, _ string) (Op, error) { return Merge(h.w, "side", MergeSquash, h.seen()) }},
		{"squash merge with conflicts", true, false, func(h *harness, _ string) (Op, error) {
			return Merge(h.w, "side", MergeSquash, h.seen())
		}},
		{"cherry-pick", false, true, func(h *harness, _ string) (Op, error) { return CherryPick(h.w, []string{side(h)}, h.seen()) }},
		{"revert", false, true, func(h *harness, _ string) (Op, error) { return Revert(h.w, []string{h.rev("HEAD")}, h.seen()) }},
		{"rebase", false, true, func(h *harness, _ string) (Op, error) { return Rebase(h.w, "side", nil, h.seen()) }},
		{"hard reset", false, true, func(h *harness, one string) (Op, error) { return Reset(h.w, one, ResetHard, h.seen()) }},
		{"commit ending a merge", true, true, func(h *harness, _ string) (Op, error) {
			h.r.Run(nil, "merge", "-q", "--no-edit", "side")
			h.r.Write("a", "resolved\n")
			h.r.Git("add", "a")
			return Commit(h.w, CommitOptions{Message: "merge side", Branch: "refs/heads/main"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, one := setup(t, c.conflict)
			op, err := c.op(h, one)
			before := h.gitDirState()
			h.do(op, err, c.ok)
			after := h.gitDirState()
			if d := diffStates(before, after); d == "" {
				t.Fatal("the write left nothing: the test shows nothing")
			}
			h.undo()
			if d := diffStates(before, h.gitDirState()); d != "" {
				t.Fatalf("after undo: %s", d)
			}
			wait(t, h.q.Redo())
			if d := diffStates(after, h.gitDirState()); d != "" {
				t.Fatalf("after redo: %s", d)
			}
			h.undo()
			if d := diffStates(before, h.gitDirState()); d != "" {
				t.Fatalf("after the second undo: %s", d)
			}
		})
	}
}

// A state file changed since the write, by a command in a terminal, is
// not the write's to put back: it stays.
func TestUndoLeavesAStateFileWrittenSince(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1\n")
	h.r.Commit("one")
	h.r.Git("switch", "-q", "-c", "side")
	h.r.Write("b", "side\n")
	h.r.Commit("side")
	h.r.Git("switch", "-q", "main")
	op, err := Merge(h.w, "side", MergeSquash, h.seen())
	h.do(op, err, true)
	h.r.Write(".git/SQUASH_MSG", "written since\n")
	h.undo()
	if h.read(".git/SQUASH_MSG") != "written since\n" {
		t.Fatalf("SQUASH_MSG %q", h.read(".git/SQUASH_MSG"))
	}
}
