package ui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/library"
)

// The picker lists the repositories the library knows under the owners
// of their remotes, marks a fork and the commits a branch has to push,
// opens the one chosen and switches to one already open.
func TestRepoPicker(t *testing.T) {
	h := newHarness(t)
	shown, fork, local := h.repoWith(), h.repoWith(), h.repoWith()
	shown.Git("remote", "add", "origin", "https://github.com/alice/shown.git")
	fork.Git("remote", "add", "origin", "git@github.com:bob/fork.git")
	fork.Git("remote", "add", "upstream", "https://github.com/carol/fork.git")
	// The fork's branch tracks origin's and is one commit ahead of it.
	fork.Git("update-ref", "refs/remotes/origin/main", "HEAD")
	fork.Git("branch", "--set-upstream-to=origin/main")
	fork.Write("more", "x")
	fork.Commit("two")
	for i, r := range []string{local.Dir, fork.Dir, shown.Dir} {
		h.lib.Opened(r, time.Unix(1_700_000_000+int64(i), 0))
	}
	h.app.Open(shown.Dir)
	h.until("the repository", h.shows("No changes"))

	h.click("Repository " + filepath.Base(shown.Dir))
	h.until("the owners", func() bool {
		return h.tt.HasText("alice") && h.tt.HasText("bob") && h.tt.HasText("Other")
	})
	h.until("the commit to push", h.describes("1 commit to push"))
	if p := h.app.repoPick.briefs[library.Clean(fork.Dir)]; !p.Fork || p.Behind != 0 {
		t.Fatalf("fork brief %+v", p)
	}
	if !h.tt.HasText("Recent") {
		t.Fatalf("no recent: %q", h.tt.Texts())
	}

	// The highlight follows the pointer on the frame it moves: the row
	// under it is drawn in the accent color.
	rowAt := map[string]ui.Rect{}
	for _, name := range []string{filepath.Base(local.Dir), filepath.Base(fork.Dir)} {
		box, ok := h.tt.Find(name) // the row, by its label
		if !ok {
			t.Fatalf("no row %s", name)
		}
		rowAt[name] = box
		h.tt.Move(box.X+box.W/2, box.Y+box.H/2) // draws the frame
		px := h.tt.Image().RGBAAt(int(box.X)+4, int(box.Y+box.H/2))
		if !isAccent(px) {
			t.Fatalf("pointer on %s, row drawn %v", name, px)
		}
	}

	// Enter opens the first repository listed: the one opened last but
	// the current.
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if len(h.app.tabs) != 2 || h.app.current().s.Path != library.Clean(fork.Dir) {
		t.Fatalf("tabs %d, current %s", len(h.app.tabs), h.app.current().s.Path)
	}
	if h.app.repoPick.open {
		t.Fatal("the picker stayed open")
	}

	// Cmd+T opens it again under the pointer, resting where the row of
	// another repository is drawn, which leaves Enter to the first row:
	// the repository opened first, open, whose tab it switches to.
	box := rowAt[filepath.Base(local.Dir)]
	h.tt.Move(box.X+box.W/2, box.Y+box.H/2)
	h.tt.Key(ui.Cmd, ui.KeyT)
	h.tt.Frame()
	if !h.app.repoPick.open {
		t.Fatal("Cmd+T did not open the picker")
	}
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if len(h.app.tabs) != 2 || h.app.current().s.Path != library.Clean(shown.Dir) {
		t.Fatalf("tabs %d, current %s", len(h.app.tabs), h.app.current().s.Path)
	}

	// A filter finds the fork, whatever row is under the pointer.
	h.tt.Key(ui.Cmd, ui.KeyT)
	h.tt.Frame()
	h.tt.Type(filepath.Base(fork.Dir))
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if len(h.app.tabs) != 2 || h.app.current().s.Path != library.Clean(fork.Dir) {
		t.Fatalf("tabs %d, current %s", len(h.app.tabs), h.app.current().s.Path)
	}
}
