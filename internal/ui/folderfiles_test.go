package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A folder git does not track lists its files, the ignored left out,
// each opening its patch; it is staged, or deleted after asking, whole.
func TestUntrackedFolderFiles(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write(".gitignore", "*.log\n")
	r.Commit("ignore")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	r.Write("newdir/top.txt", "top\n")
	r.Write("newdir/sub/deep.txt", "deep line\n")
	r.Write("newdir/noise.log", "x\n")
	r.Write("gone/x.txt", "x\n")
	h.until("the folders", func() bool { return h.tt.HasText("newdir/") && h.tt.HasText("gone/") })
	h.click("newdir/")
	h.until("its files", func() bool { return h.tt.HasText("2 files") && h.tt.HasText("sub/deep.txt") })
	if h.tt.HasText("noise.log") {
		t.Fatal("an ignored file is listed")
	}
	tb := h.app.current()
	pv := &tb.cv.diff.pv
	// The first file is chosen, and the one clicked after it.
	h.until("the first file's patch", patchHas(pv, "deep line", ""))
	h.click("top.txt")
	h.until("the patch of the file clicked", patchHas(pv, "top", ""))

	h.click("Stage Folder")
	h.until("the folder staged", func() bool { return strings.Contains(r.Git("diff", "--cached", "--name-only"), "newdir/top.txt") })

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

// A folder whose files pass the bound lists the first and counts all.
func TestUntrackedFolderBound(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	for i := range folderListed + 3 {
		name := filepath.Join(r.Dir, "many", "f"+strconv.Itoa(i))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.until("the folder", h.shows("many/"))
	h.click("many/")
	h.until("the count", h.shows("5,000 of 5,003 files"))
	if n := len(h.app.current().cv.folder.files.Paths); n != folderListed {
		t.Fatalf("%d listed", n)
	}
}
