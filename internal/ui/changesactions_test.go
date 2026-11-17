package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// With a filter typed, the section's button stages the files shown and
// says so; the Stage All command stages all.
func TestStageAllWithAFilter(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.go", "b.txt", "c.txt")
	r.Commit("files")
	for _, f := range []string{"a.go", "b.txt", "c.txt"} {
		r.Write(f, "changed\n")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the changes", h.shows("c.txt"))
	cv := &h.app.current().cv
	cv.filter = "txt"
	h.until("the filter", h.shows("Stage 2 Shown"))
	h.click("Stage 2 Shown")
	h.until("the two shown staged", func() bool { return cv.model != nil && len(cv.model.all(secStaged)) == 2 })
	if got := len(cv.model.all(secUnstaged)); got != 1 {
		t.Fatalf("%d left unstaged", got)
	}
	h.app.runCommand("stage-all")
	h.until("all staged", func() bool { return cv.model != nil && len(cv.model.all(secStaged)) == 3 })
}

// The first file is chosen on opening, so its patch shows; its header
// stages and unstages it whole.
func TestPatchHeaderStagesTheFile(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt")
	r.Commit("files")
	r.Write("a.txt", "changed\n")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the patch of the first file", h.shows("Stage File"))
	cv := &h.app.current().cv
	h.click("Stage File")
	h.until("staged", func() bool { return cv.model != nil && len(cv.model.files[secStaged]) == 1 })
	h.until("the staged patch", h.shows("Unstage File"))
	h.click("Unstage File")
	h.until("unstaged", func() bool { return cv.model != nil && len(cv.model.files[secUnstaged]) == 1 })
}

// A clean tree offers what there is to do instead of a blank pane.
func TestCleanTreePane(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt")
	r.Commit("files")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the clean tree", func() bool { return h.tt.HasText("No local changes") && h.tt.HasText("Reveal in File Manager") })
}

// A rename's header names the path it had.
func TestRenameHeader(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("old.txt")
	r.Commit("files")
	r.Git("mv", "old.txt", "new.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the rename", h.shows("old.txt → new.txt"))
}

// Ignoring white space drops line staging, and says so with the way
// back.
func TestWhitespaceNotice(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "one\ntwo\n")
	r.Commit("files")
	r.Write("a.txt", "one\n  two\nthree\n")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the patch", h.shows("Stage File"))
	h.app.diffOpts().ws = 1
	h.until("the notice", h.shows("Lines can be staged only with all whitespace shown"))
	h.click("Show All Whitespace")
	h.until("all white space", func() bool {
		return h.app.diffOpts().ws == 0 && !h.tt.HasText("Lines can be staged only with all whitespace shown")
	})
}

// A binary file's patch says its sizes and offers no options of text.
func TestBinaryPatch(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("blob.bin", "\x00\x01\x02")
	r.Commit("files")
	r.Write("blob.bin", "\x00\x01\x02\x03\x04")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the sizes", h.shows("Binary file, 3 bytes to 5 bytes"))
	if h.tt.HasText("No wrap") || h.tt.HasText("Show All Whitespace") {
		t.Fatalf("options of text on a binary patch: %q", h.tt.Texts())
	}
}

// A small picture shows larger by whole times; a large one at its size.
func TestPictureScale(t *testing.T) {
	for _, c := range []struct{ w, h, want float32 }{{16, 16, 8}, {64, 32, 4}, {100, 100, 2}, {200, 50, 1}, {1024, 768, 1}} {
		if got := pictureScale(c.w, c.h); got != c.want {
			t.Errorf("%v x %v: %v, want %v", c.w, c.h, got, c.want)
		}
	}
}

// Enter stages every file picked, as Delete discards them all, not only
// the one the keyboard is on.
func TestStagingActsOnEveryFilePicked(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt", "b.txt", "c.txt")
	r.Commit("files")
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		r.Write(f, "changed\n")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the changes", h.shows("c.txt"))
	cv := &h.app.current().cv
	h.click("a.txt")
	h.tt.Key(ui.Shift, ui.KeyDown)
	h.tt.Key(ui.Shift, ui.KeyDown)
	h.until("three picked", func() bool { return cv.picked.Len() == 3 })
	h.tt.Key(0, ui.KeyEnter)
	h.until("all three staged", func() bool {
		m := cv.model
		return m != nil && len(m.files[secStaged]) == 3 && len(m.files[secUnstaged]) == 0
	})
}
