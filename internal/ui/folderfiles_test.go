package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/session"
	"ramus/locales"
)

// A folder git does not track lists its files under it, the ignored left
// out, each counted and opening its patch as a file does; it opens and
// closes as a folder of the tree does, a file of it is staged or deleted
// alone, and the folder whole from its pane.
func TestUntrackedFolderFiles(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write(".gitignore", "*.log\n")
	r.Commit("ignore")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("newdir/top.txt", "top\n")
	r.Write("newdir/sub/deep.txt", "deep line\nmore\n")
	r.Write("newdir/noise.log", "x\n")
	r.Write("gone/x.txt", "x\n")
	r.Write("gone/y.txt", "y\n")
	h.until("the folders' files", func() bool {
		_, ok := h.tt.Access("untracked newdir/sub/deep.txt, 2 lines added, 0 deleted")
		return ok && hasExact(h, "top.txt") && hasExact(h, "x.txt")
	})
	if h.tt.HasText("noise.log") {
		t.Fatal("an ignored file is listed")
	}
	if _, ok := h.tt.Access("untracked folder newdir/, open"); !ok {
		t.Fatalf("the folder's label: %q", h.tt.Texts())
	}
	// The folder's count is its files'.
	if countExact(h, "+3") == 0 {
		t.Fatalf("no count of the folder: %q", h.tt.Texts())
	}
	// The changes are counted in files, a folder being those it holds,
	// the ignored left out: two entries of status, four files.
	h.until("the files counted", func() bool { return h.tt.HasText("Changes (4)") && h.tt.HasText("Unstaged (4)") })
	if _, ok := h.tt.Access("Unstaged, 4 files"); !ok {
		t.Fatalf("the section's label: %q", h.tt.Texts())
	}
	tb := h.app.current()
	pv := &tb.cv.diff.pv
	h.click("sub/deep.txt")
	h.until("the patch of a file in it", patchHas(pv, "deep line", ""))
	h.click("top.txt")
	h.until("the patch of the file clicked", patchHas(pv, "top", ""))

	// Left and Right close and open it, from its row.
	h.click("newdir/")
	h.until("its pane", h.shows("2 files"))
	h.tt.Key(0, ui.KeyLeft)
	h.until("closed by Left", func() bool { return !hasExact(h, "top.txt") })
	if _, ok := h.tt.Access("untracked folder newdir/, closed"); !ok {
		t.Fatalf("the folder's label does not say it is closed: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyRight)
	h.until("opened by Right", func() bool { return hasExact(h, "top.txt") })

	// A filter keeps the folder for a file of it, and only that file.
	tb.cv.filter = "deep"
	h.until("the filter", func() bool { return h.tt.HasText("sub/deep.txt") && !hasExact(h, "top.txt") && !h.tt.HasText("gone/") })
	tb.cv.filter = ""
	h.until("no filter", func() bool { return hasExact(h, "top.txt") })

	// A file of it staged alone, by Enter.
	h.click("top.txt")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the file staged", func() bool { return r.Git("diff", "--cached", "--name-only") == "newdir/top.txt\n" })
	// Its folder is no longer untracked whole: git lists what is left.
	h.until("the rest", func() bool { return hasExact(h, "newdir/sub/") })
	h.click("newdir/sub/")
	h.click("Stage Folder")
	h.until("the folder staged", func() bool { return strings.Contains(r.Git("diff", "--cached", "--name-only"), "newdir/sub/deep.txt") })

	// A file of the other deleted alone, then the folder.
	h.click("x.txt")
	h.tt.Key(0, ui.KeyDelete)
	h.until("the question", h.shows("Discard 1 file?"))
	h.click("Discard")
	h.until("the file deleted", func() bool {
		_, err := os.Stat(filepath.Join(r.Dir, "gone", "x.txt"))
		return os.IsNotExist(err) && hasExact(h, "y.txt") && !hasExact(h, "x.txt")
	})
	h.click("gone/")
	h.until("the other folder", h.shows("1 file"))
	h.click("Discard Folder…")
	h.until("the question", h.shows("Discard 1 file?"))
	h.click("Discard")
	h.until("the folder deleted", func() bool {
		_, err := os.Stat(filepath.Join(r.Dir, "gone"))
		return os.IsNotExist(err)
	})
}

// In the tree, a new folder's files are under it as a folder's are.
func TestUntrackedFolderInTree(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("app/main.go", "package main\n")
	r.Commit("app")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("app/new/a.go", "package a\n")
	r.Write("app/new/b/c.go", "package c\n")
	h.click("Tree")
	h.until("the files", func() bool { return hasExact(h, "a.go") && hasExact(h, "b/c.go") })
	m := h.app.current().cv.model
	depth := map[string]uint16{}
	for _, row := range m.rows {
		depth[m.name(row)] = row.depth
	}
	if depth["app"] != 0 || depth["new/"] != 1 || depth["a.go"] != 2 || depth["b/c.go"] != 2 {
		t.Fatalf("depths %v", depth)
	}
}

// A folder whose files pass the bound lists the first and counts the
// rest, on a row of its own; its pane counts all.
func TestUntrackedFolderBound(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	if err := os.MkdirAll(filepath.Join(r.Dir, "many"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range session.FolderListed + 3 {
		if err := os.WriteFile(filepath.Join(r.Dir, "many", "f"+strconv.Itoa(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	more := locales.ChangesFolderMore(3)
	inner := func() (n int, last string) {
		m := h.app.current().cv.model
		if m == nil {
			return 0, ""
		}
		for _, row := range m.rows {
			if row.kind == rowInner {
				n++
			}
		}
		if len(m.rows) > 0 {
			last = rowLabel(&h.app.current().cv, m, m.rows[len(m.rows)-1], h.app.current().s.State.Folders)
		}
		return n, last
	}
	h.until("the files listed", func() bool { _, last := inner(); return last == more })
	if n, _ := inner(); n != session.FolderListed {
		t.Fatalf("%d listed", n)
	}
	h.click("many/")
	h.until("the count", h.shows("1,003 files"))
}

// A file of a new folder staged, the next file of it is chosen, though
// the folder's own row goes with the write: git lists the rest of it.
func TestUntrackedFolderStagedInTurn(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		r.Write("new/"+n+".txt", n+"\n")
	}
	h.until("the files", func() bool { return hasExact(h, "e.txt") })
	h.click("c.txt")
	h.tt.Key(0, ui.KeyEnter)
	h.until("c staged", func() bool { return r.Git("diff", "--cached", "--name-only") == "new/c.txt\n" })
	h.until("the next chosen", func() bool {
		f, ok := h.app.current().cv.chosenFile()
		m := h.app.current().cv.model
		return ok && m.path(f) == "new/d.txt" && hasExact(h, "new/e.txt")
	})
}

// With a filter that matches files of a new folder, not the folder, what
// stages the files shown stages those alone.
func TestUntrackedFolderFiltered(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("new/foo.txt", "foo\n")
	r.Write("new/bar.txt", "bar\n")
	h.until("the files", func() bool { return hasExact(h, "bar.txt") })
	h.app.current().cv.filter = "foo"
	h.until("the filter", func() bool { return hasExact(h, "foo.txt") && !hasExact(h, "bar.txt") })
	h.click(locales.ChangesStageShown(1))
	h.until("foo staged", func() bool { return r.Git("diff", "--cached", "--name-only") == "new/foo.txt\n" })
	if s := r.Git("status", "--porcelain"); !strings.Contains(s, "?? new/bar.txt") {
		t.Fatalf("bar staged too: %q", s)
	}
}

// The folder's own row, kept by a filter for a file of it, acts on the
// files shown alone, as the section's header does: its menu's discard
// and its pane's Stage Folder leave the files the filter hides.
func TestUntrackedFolderRowFiltered(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	r.Write("new/foo.txt", "foo\n")
	r.Write("new/bar.txt", "bar\n")
	r.Write("other/foo.txt", "foo\n")
	r.Write("other/baz.txt", "baz\n")
	h.until("the files", func() bool { return hasExact(h, "bar.txt") && hasExact(h, "baz.txt") })
	tb.cv.filter = "foo"
	h.until("the filter", func() bool { return hasExact(h, "foo.txt") && !hasExact(h, "bar.txt") && !hasExact(h, "baz.txt") })
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(r.Dir, p)); return err == nil }

	h.tt.RightClick("new/")
	if err := h.tt.ChooseMenuItem(locales.ChangesDiscardFiles(1)); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", func() bool { return tb.cv.confirm.open })
	h.click(locales.ChangesDiscard())
	h.until("foo deleted", func() bool { return !exists("new/foo.txt") })
	h.idle(tb, "the delete")
	if !exists("new/bar.txt") {
		t.Fatal("the folder's row deleted a file the filter hid")
	}

	h.until("other listed", func() bool { return h.tt.HasText("other/") })
	h.click("other/")
	h.click(locales.ChangesStageFolder())
	h.until("foo staged", func() bool { return strings.Contains(r.Git("diff", "--cached", "--name-only"), "other/foo.txt") })
	h.idle(tb, "the stage")
	if s := r.Git("status", "--porcelain"); !strings.Contains(s, "?? other/baz.txt") {
		t.Fatalf("the folder's pane staged a file the filter hid: %q", s)
	}
}
