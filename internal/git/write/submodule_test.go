package write

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// git is the oracle: its config and submodule status say what each op
// did.
func TestSubmodules(t *testing.T) {
	h := newHarness(t)
	sub := gittest.Init(t)
	sub.Write("s", "1")
	first := sub.Commit("one")
	h.r.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "lib/one")
	h.r.Commit("with a submodule")
	sub.Write("s", "2")
	sub.Commit("two")
	// Deinitialized: neither registered nor checked out, as in a fresh
	// clone.
	h.r.Git("submodule", "deinit", "-q", "-f", "lib/one")
	c, w := h.r, h.w
	// Local paths stand in for a remote here; git refuses them for
	// submodules unless told.
	local := func(op Op) Op {
		op.Cmd.Env = append(op.Cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.file.allow", "GIT_CONFIG_VALUE_0=always")
		return op
	}

	h.do(SubmoduleInit(w, []string{"lib/one"}), nil, true)
	if url := strings.TrimSpace(c.Git("config", "submodule.lib/one.url")); url != sub.Dir {
		t.Fatalf("url %q", url)
	}
	h.do(local(SubmoduleUpdate(w, nil, false, false)), nil, true)
	if st := c.Git("submodule", "status"); !strings.HasPrefix(st, " "+first) {
		t.Fatalf("status %q", st)
	}

	// The URL moves in .gitmodules: sync carries it into the config and
	// the submodule's remote.
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(sub.Dir, moved); err != nil {
		t.Fatal(err)
	}
	c.Git("config", "--file", ".gitmodules", "submodule.lib/one.url", moved)
	h.do(SubmoduleSync(w, []string{"lib/one"}, false), nil, true)
	if url := strings.TrimSpace(c.Git("-C", "lib/one", "remote", "get-url", "origin")); url != moved {
		t.Fatalf("submodule's remote %q", url)
	}

	// Update with init, of a path that is not a submodule: git says so.
	h.do(local(SubmoduleUpdate(w, []string{"nope"}, true, true)), nil, false)
}

// With submodule.recurse set, discard, restore, hard reset and their undo
// still leave the files inside a submodule alone: the snapshot taken
// first holds the submodule's commit, not its files.
func TestPuttingBackLeavesSubmodulesAlone(t *testing.T) {
	h := newHarness(t)
	sub := gittest.Init(t)
	sub.Write("s.txt", "s\n")
	sub.Commit("s")
	h.r.Write("x", "x\n")
	h.r.Commit("x")
	h.r.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "sub")
	h.r.Commit("sub")
	h.r.Git("config", "submodule.recurse", "true")
	inside := filepath.Join(h.r.Dir, "sub", "s.txt")
	mine := func(what string) {
		t.Helper()
		if b, _ := os.ReadFile(inside); string(b) != "MY SUB WORK\n" {
			t.Fatalf("%s: the work inside the submodule is now %q", what, b)
		}
	}
	if err := os.WriteFile(inside, []byte("MY SUB WORK\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.do(Discard(h.w, []string{"sub"}), nil, true)
	mine("discard")
	h.undo()
	mine("undo of the discard")
	op, err := RestoreFrom(h.w, "sub", h.rev("HEAD"))
	h.do(op, err, true)
	mine("restore")
	h.r.Write("x", "x changed\n")
	op, err = Reset(h.w, h.rev("HEAD"), ResetHard, h.seen())
	h.do(op, err, true)
	mine("hard reset")
	h.undo()
	mine("undo of the hard reset")
}
