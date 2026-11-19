package write

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// exists reports whether anything, a link included, is at p.
func (h *harness) exists(p string) bool {
	_, err := os.Lstat(filepath.Join(h.r.Dir, filepath.FromSlash(p)))
	return err == nil
}

// restoreGone commits files, removes them in a second commit, and
// returns the first.
func (h *harness) restoreGone(files map[string]string, dir string) string {
	h.t.Helper()
	for p, c := range files {
		h.r.Write(p, c)
	}
	h.r.Write("top", "t\n")
	h.r.Git("add", "-f", "--", ".")
	first := h.r.Commit("one")
	h.r.Git("rm", "-q", "-r", "--", dir)
	h.r.Commit("two")
	return first
}

// Undo of a restore removes what the restore made and nothing else: a
// file the user made after it, which a .gitignore the restore made
// hides, stays, as no snapshot holds it.
func TestUndoKeepsAFileHiddenByARuleTheWriteMade(t *testing.T) {
	h := newHarness(t)
	first := h.restoreGone(map[string]string{"cfg/.gitignore": "local.env\n", "cfg/x": "x\n"}, "cfg")
	op, err := RestoreFrom(h.w, "cfg", first)
	h.do(op, err, true)
	h.r.Write("cfg/local.env", "SECRET=1\n")
	h.undo()
	if h.read("cfg/local.env") != "SECRET=1\n" {
		t.Fatal("undo removed a file the user made after the write")
	}
	if h.exists("cfg/x") || h.exists("cfg/.gitignore") {
		t.Fatal("undo left what the restore made")
	}
}

// A file there before, ignored by a rule the restore took away, is not
// the restore's: its undo puts the rule back and leaves the file.
func TestUndoKeepsAFileARuleTheWriteRemovedHid(t *testing.T) {
	h := newHarness(t)
	h.r.Write("cfg/x", "x\n")
	h.r.Write("top", "t\n")
	first := h.r.Commit("one")
	h.r.Write("cfg/.gitignore", "local.env\n")
	h.r.Commit("rule")
	h.r.Write("cfg/local.env", "SECRET=1\n")
	op, err := RestoreFrom(h.w, "cfg", first)
	h.do(op, err, true)
	if h.exists("cfg/.gitignore") {
		t.Fatal("the restore kept the rule: the test shows nothing")
	}
	h.undo()
	if h.read("cfg/local.env") != "SECRET=1\n" || h.read("cfg/.gitignore") != "local.env\n" {
		t.Fatalf("local.env %q, .gitignore %q", h.read("cfg/local.env"), h.read("cfg/.gitignore"))
	}
}

// An ignored file the restore made is removed by its undo, unless the
// user changed it since: then it stays, the undo says so, and it is
// done all the same, its entry moving to redo.
func TestUndoLeavesAMadeFileChangedSince(t *testing.T) {
	h := newHarness(t)
	h.r.Write(".gitignore", "*.log\n")
	first := h.restoreGone(map[string]string{"cfg/x": "x\n", "cfg/a.log": "made\n", "cfg/b.log": "made\n"}, "cfg")
	op, err := RestoreFrom(h.w, "cfg", first)
	h.do(op, err, true)
	h.r.Write("cfg/b.log", "edited since\n")
	_, err = wait(t, h.q.Undo())
	var le *leftError
	if !errors.As(err, &le) || strings.Join(le.paths, ",") != "cfg/b.log" {
		t.Fatalf("undo: %v", err)
	}
	if h.read("cfg/b.log") != "edited since\n" || h.exists("cfg/a.log") || h.exists("cfg/x") {
		t.Fatalf("b.log %q, a.log there %v, x there %v", h.read("cfg/b.log"), h.exists("cfg/a.log"), h.exists("cfg/x"))
	}
	if u, r := h.q.History.Next(); u != "" || r != op.Name {
		t.Fatalf("next undo %q, redo %q", u, r)
	}
}

// Undo, redo and undo again remove what each run made; a file the user
// made in a folder the write made, or a made file edited, makes the undo
// refuse, before anything changes.
func TestUndoRedoUndoOfARestore(t *testing.T) {
	h := newHarness(t)
	first := h.restoreGone(map[string]string{"old/x": "x\n"}, "old")
	op, err := RestoreFrom(h.w, "old", first)
	h.do(op, err, true)
	h.undo()
	if h.exists("old") {
		t.Fatal("undo left old")
	}
	if _, err := wait(t, h.q.Redo()); err != nil || !h.exists("old/x") {
		t.Fatalf("redo: %v", err)
	}
	h.r.Write("old/mine", "mine\n")
	var se *StaleError
	if _, err := wait(t, h.q.Undo()); !errors.As(err, &se) || h.read("old/mine") != "mine\n" {
		t.Fatalf("undo over a file made since: %v", err)
	}
	os.Remove(filepath.Join(h.r.Dir, "old/mine"))
	h.r.Write("old/x", "edited\n")
	if _, err := wait(t, h.q.Undo()); !errors.As(err, &se) || h.read("old/x") != "edited\n" {
		t.Fatalf("undo over a made file edited: %v", err)
	}
	h.r.Write("old/x", "x\n")
	h.undo()
	if h.exists("old") {
		t.Fatal("undo after redo left old")
	}
}

// A folder the restore made, replaced since by a link to a folder
// outside: nothing outside is removed.
func TestUndoDoesNotRemoveThroughALink(t *testing.T) {
	h := newHarness(t)
	first := h.restoreGone(map[string]string{"old/x": "x\n"}, "old")
	op, err := RestoreFrom(h.w, "old", first)
	h.do(op, err, true)
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "x"), []byte("x\n"), 0o644)
	os.RemoveAll(filepath.Join(h.r.Dir, "old"))
	if err := os.Symlink(out, filepath.Join(h.r.Dir, "old")); err != nil {
		t.Skip(err)
	}
	wait(t, h.q.Undo())
	if _, err := os.Stat(filepath.Join(out, "x")); err != nil {
		t.Fatal("undo removed a file outside the work tree")
	}
}

// Names with a line break or a tab are removed as they are, and names
// that only start the same stay.
func TestUndoOfARestoreWithOddNames(t *testing.T) {
	h := newHarness(t)
	dir := "o\nld"
	if !gittest.Portable(dir + "/x\ty") {
		t.Skip("no such names here")
	}
	first := h.restoreGone(map[string]string{dir + "/x\ty": "x\n"}, dir)
	h.r.Write("o", "decoy\n")
	h.r.Write("o\nld2/z", "decoy\n")
	op, err := RestoreFrom(h.w, dir, first)
	h.do(op, err, true)
	if !h.exists(dir + "/x\ty") {
		t.Fatal("not restored")
	}
	h.undo()
	if h.exists(dir) || !h.exists("o") || !h.exists("o\nld2/z") {
		t.Fatalf("%q there %v; decoys %v %v", dir, h.exists(dir), h.exists("o"), h.exists("o\nld2/z"))
	}
}

// A restore that fails part way, after making some files, is undone
// whole: what it made goes.
func TestUndoOfARestoreThatFailedPartWay(t *testing.T) {
	h := newHarness(t)
	first := h.restoreGone(map[string]string{"cfg/.gitattributes": "b filter=broken\n", "cfg/a": "a\n", "cfg/b": "b\n"}, "cfg")
	h.r.Git("config", "filter.broken.clean", "cat")
	h.r.Git("config", "filter.broken.smudge", "false")
	h.r.Git("config", "filter.broken.required", "true")
	op, err := RestoreFrom(h.w, "cfg", first)
	h.do(op, err, false)
	if !h.exists("cfg/a") {
		t.Fatal("the restore made nothing before it failed: the test shows nothing")
	}
	h.undo()
	if h.exists("cfg") {
		t.Fatal("undo left what the failed restore made")
	}
}

// A restore that writes over an ignored file, as the commit it takes the
// folder from had one by that name, keeps it first, and its undo brings
// it back.
func TestRestoreKeepsTheIgnoredFileItWritesOver(t *testing.T) {
	h := newHarness(t)
	h.r.Write(".gitignore", "*.env\n")
	first := h.restoreGone(map[string]string{"cfg/x": "x\n", "cfg/app.env": "committed\n"}, "cfg")
	h.r.Write("cfg/app.env", "mine\n")
	op, err := RestoreFrom(h.w, "cfg", first)
	h.do(op, err, true)
	if h.read("cfg/app.env") != "committed\n" {
		t.Fatal("the restore did not write over it: the test shows nothing")
	}
	h.undo()
	if h.read("cfg/app.env") != "mine\n" || h.exists("cfg/x") {
		t.Fatalf("app.env %q, x there %v", h.read("cfg/app.env"), h.exists("cfg/x"))
	}
}

// A repository the user put since in a folder a restore made, where the
// commit had a submodule, stays.
func TestUndoOfARestoreLeavesARepositoryInside(t *testing.T) {
	h := newHarness(t)
	h.r.Write("top", "t\n")
	h.r.Write("cfg/x", "x\n")
	h.r.Git("add", ".")
	h.r.Git("update-index", "--add", "--cacheinfo", "160000,"+h.r.Commit("zero")+",cfg/sub")
	first := h.r.Commit("one")
	h.r.Git("rm", "-q", "-r", "--cached", "cfg")
	os.RemoveAll(filepath.Join(h.r.Dir, "cfg"))
	h.r.Commit("two")
	op, err := RestoreFrom(h.w, "cfg", first)
	h.do(op, err, true)
	h.r.Git("init", "-q", filepath.Join(h.r.Dir, "cfg", "sub"))
	os.WriteFile(filepath.Join(h.r.Dir, "cfg", "sub", "work"), []byte("work\n"), 0o644)
	wait(t, h.q.Undo())
	if h.read("cfg/sub/work") != "work\n" {
		t.Fatal("undo removed a repository the user made")
	}
}
