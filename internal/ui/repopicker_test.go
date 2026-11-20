package ui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gittest"
	"ramus/internal/library"
)

// The repositories open in the window are its tabs: the picker heads
// with the recent ones not open, and lists the open ones with the others,
// each with its branch and a dot while it has changes.
func TestRepoPickerLeavesOpenToTabs(t *testing.T) {
	h := newHarness(t)
	one, two, closed := h.repoWith(), h.repoWith(), h.repoWith()
	two.Git("checkout", "-q", "-b", "topic")
	for i, r := range []*gittest.Repo{closed, one, two} {
		h.trust.Set(r.GitDir(), true)
		h.lib.Opened(r.Dir, time.Unix(1_700_000_000+int64(i), 0))
	}
	h.app.Open(one.Dir)
	h.app.Open(two.Dir)
	h.until("both open", func() bool {
		return len(h.app.tabs) == 2 && h.app.tabs[0].s.State.Status != nil && h.app.tabs[1].s.State.Status != nil
	})
	one.Write("README", "changed")
	h.until("the change", func() bool { st := h.app.tabs[0].s.State.Status; return st != nil && st.Len() > 0 })

	h.click("Repository " + filepath.Base(two.Dir))
	h.until("the recent group", func() bool { return hasExact(h, "Recent") })
	var names []string
	for _, r := range h.app.repoRows() {
		if r.heading != "" {
			names = append(names, "# "+r.heading)
		} else {
			names = append(names, filepath.Base(r.path))
		}
	}
	want := []string{"# Recent", filepath.Base(closed.Dir)}
	if len(names) < len(want) || !slices.Equal(names[:len(want)], want) {
		t.Fatalf("rows %q", names)
	}
	for _, r := range []*gittest.Repo{one, two} {
		if !slices.Contains(names[len(want):], filepath.Base(r.Dir)) {
			t.Fatalf("open %s not listed: rows %q", filepath.Base(r.Dir), names)
		}
	}
	h.until("the dot for the changes", h.describes("Uncommitted changes"))
}

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
	h.until("the repository", h.shows("No local changes"))

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
	// the recent repository not open, which it opens. The open ones are
	// the tabs'.
	box := rowAt[filepath.Base(fork.Dir)]
	h.tt.Move(box.X+box.W/2, box.Y+box.H/2)
	h.tt.Key(ui.Cmd, ui.KeyT)
	h.tt.Frame()
	if !h.app.repoPick.open {
		t.Fatal("Cmd+T did not open the picker")
	}
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if len(h.app.tabs) != 3 || h.app.current().s.Path != library.Clean(local.Dir) {
		t.Fatalf("tabs %d, current %s", len(h.app.tabs), h.app.current().s.Path)
	}

	// A filter finds the fork, whatever row is under the pointer.
	h.tt.Key(ui.Cmd, ui.KeyT)
	h.tt.Frame()
	h.tt.Type(filepath.Base(fork.Dir))
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if len(h.app.tabs) != 3 || h.app.current().s.Path != library.Clean(fork.Dir) {
		t.Fatalf("tabs %d, current %s", len(h.app.tabs), h.app.current().s.Path)
	}
}

// A worktree added to a repository is not one of the library's: opened,
// it is listed while open, and not kept. One a library written before
// holds, and a folder gone, are left out.
func TestRepoPickerLeavesOutWorktrees(t *testing.T) {
	h := newHarness(t)
	main, other := h.repoWith(), h.repoWith()
	main.Git("remote", "add", "origin", "https://github.com/alice/main.git")
	old := filepath.Join(t.TempDir(), "old-worktree")
	now := filepath.Join(t.TempDir(), "new-worktree")
	t.Cleanup(h.closeAll) // before the folders above go
	main.Git("worktree", "add", "-q", old, "-b", "old")
	main.Git("worktree", "add", "-q", now, "-b", "new")
	// A folder an agent's worktree was in, gone since: inside the
	// repository, which reading it would find instead.
	gone := filepath.Join(main.Dir, "agents", "gone-worktree")
	for i, p := range []string{other.Dir, gone, old, main.Dir} {
		h.lib.Opened(p, time.Unix(1_700_000_000+int64(i), 0))
	}
	for _, r := range []*gittest.Repo{main, other} {
		h.trust.Set(r.GitDir(), true)
	}
	h.app.Open(main.Dir)
	h.until("the repository", h.shows("No local changes"))
	names := func() []string {
		var names []string
		for _, r := range h.app.repoRows() {
			if r.heading == "" {
				names = append(names, filepath.Base(r.path))
			}
		}
		return names
	}
	read := func(paths ...string) func() bool {
		return func() bool {
			for _, p := range paths {
				if _, ok := h.app.repoPick.briefs[library.Clean(p)]; !ok {
					return false
				}
			}
			return true
		}
	}

	h.click("Repository " + filepath.Base(main.Dir))
	h.until("the briefs", read(main.Dir, other.Dir, gone, old))
	if b := h.app.repoPick.briefs; !b[library.Clean(old)].Worktree || !b[library.Clean(gone)].Gone || b[library.Clean(gone)].Owner != "" ||
		b[library.Clean(main.Dir)].Worktree || b[library.Clean(main.Dir)].Gone {
		t.Fatalf("briefs %+v", b)
	}
	for _, name := range names() {
		if name == "old-worktree" || name == "gone-worktree" {
			t.Fatalf("rows %q", names())
		}
	}
	if got := names(); !slices.Contains(got, filepath.Base(main.Dir)) || !slices.Contains(got, filepath.Base(other.Dir)) {
		t.Fatalf("rows %q", got)
	}
	// Nor does a search find them.
	h.tt.Type("-worktree")
	h.tt.Frame()
	if got := names(); len(got) != 0 {
		t.Fatalf("found %q", got)
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()

	// Opened, a worktree is a tab to switch to, under its owner, and the
	// library is not told of it.
	h.app.Open(now)
	h.until("the worktree open", func() bool { return len(h.app.tabs) == 2 && h.app.tabs[1].s.State.Status != nil })
	h.app.writes.Wait()
	for _, r := range h.lib.Recent(100) {
		if r.Path == library.Clean(now) {
			t.Fatal("the library keeps a worktree opened")
		}
	}
	h.app.openRepoPicker()
	h.until("its brief", read(now))
	h.tt.Frame()
	if got := names(); !slices.Contains(got, "new-worktree") || slices.Contains(got, "old-worktree") {
		t.Fatalf("rows with a worktree open %q", got)
	}
	at := slices.IndexFunc(h.app.repoRows(), func(r repoRow) bool { return r.path == library.Clean(now) })
	if rows := h.app.repoRows(); rows[at-1].heading != "alice" && rows[at-2].heading != "alice" {
		t.Fatalf("the worktree is not under its owner: %+v", rows)
	}
}

// A query typed in the repository picker scrolled down, the pointer off
// its rows, brings the row Enter takes into view.
func TestRepoPickerShowsTheRowEnterTakes(t *testing.T) {
	h := newHarness(t)
	shown := h.repoWith()
	parent, gitBin := t.TempDir(), gittest.GitPath(t)
	for i := range 30 {
		dir := filepath.Join(parent, fmt.Sprintf("repo-%02d", i))
		if out, err := exec.Command(gitBin, "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		h.lib.Opened(dir, time.Unix(1_700_000_000+int64(i), 0))
	}
	h.app.Open(shown.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.click("Repository " + filepath.Base(shown.Dir))
	p := &h.app.repoPick
	h.until("the repositories", func() bool { return hasExact(h, "repo-00") || hasExact(h, "repo-29") })
	h.scrollDown(&p.list, "Repositories", 15)
	sf, _ := h.tt.Find("Search repositories")
	h.tt.Move(sf.X+sf.W/2, sf.Y+sf.H/2)
	h.tt.Frame()
	h.tt.Type("repo-")
	h.tt.Frame()
	h.tt.Frame()
	rows := h.app.repoRows()
	at := slices.IndexFunc(rows, func(r repoRow) bool { return r.key == p.hi })
	first, last := p.list.Visible()
	if at < 0 || at < first || at > last {
		t.Fatalf("Enter would take row %d of %d, which the list (rows %d to %d) does not show", at, len(rows), first, last)
	}
}
