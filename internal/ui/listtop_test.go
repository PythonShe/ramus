package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/lfsserver"
)

// A keyed list whose rows change above its top: while it shows its top it
// shows what came there, and scrolled away from it it keeps the row the
// user scrolled to at its top.

// top returns the row at the top of l as last laid out.
func top(l *ui.ListState) int {
	first, _ := l.Visible()
	return first
}

// scrollDown scrolls the list named name down by rows rows, as the user
// does with a touchpad, and waits until it is scrolled.
func (h *harness) scrollDown(l *ui.ListState, name string, rows int) {
	h.t.Helper()
	h.until("the list "+name, func() bool { _, ok := h.tt.Access(name); return ok })
	a, _ := h.tt.Access(name)
	x, y := a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2
	h.tt.Scroll(x, y, 0, float32(rows*rowHeight))
	h.until("the list scrolled", func() bool { return top(l) > 0 })
}

// rowsComeAbove checks list l, named name, as add puts rows above the row
// at its top, which added reports once the list has them: shown from its
// top, it shows them; scrolled down by scroll rows, it keeps the row the
// user scrolled to at its top.
func (h *harness) rowsComeAbove(l *ui.ListState, name string, scroll int, add func(), added func() bool) {
	h.t.Helper()
	first, was := h.placeThen(l, name, scroll, add, added)
	switch got := top(l); {
	case scroll == 0 && got != 0:
		h.t.Fatalf("%s showed its top; with rows above it, it shows row %d", name, got)
	case scroll > 0 && l.Key(got) != was:
		h.t.Fatalf("%s was scrolled to %v at row %d; now row %d, %v, is at its top", name, was, first, got, l.Key(got))
	}
}

// rowsMoveUnder is rowsComeAbove for a list whose rows are sorted again
// and filtered: scrolled, it stays where it is scrolled.
func (h *harness) rowsMoveUnder(l *ui.ListState, name string, scroll int, add func(), added func() bool) {
	h.t.Helper()
	first, _ := h.placeThen(l, name, scroll, add, added)
	if got := top(l); got != first {
		h.t.Fatalf("%s showed row %d at its top; with rows above it, it shows row %d", name, first, got)
	}
}

// placeThen scrolls l down by scroll rows, or waits for it at its top,
// and runs add until added: it returns the row that was at the top, and
// its key.
func (h *harness) placeThen(l *ui.ListState, name string, scroll int, add func(), added func() bool) (int, any) {
	h.t.Helper()
	if scroll == 0 {
		h.until("the list at its top", func() bool { return top(l) == 0 })
	} else {
		h.scrollDown(l, name, scroll)
	}
	first := top(l)
	was := l.Key(first)
	add()
	h.until("the rows above", added)
	h.tt.Frame()
	h.tt.Frame()
	return first, was
}

// uniqueKeys fails unless l's Key gives each of its n rows a key of its
// own, as mygo asks.
func uniqueKeys(t *testing.T, l *ui.ListState, n int) {
	t.Helper()
	seen := map[any]int{}
	for i := range n {
		k := l.Key(i)
		if j, ok := seen[k]; ok {
			t.Fatalf("rows %d and %d share the key %v", j, i, k)
		}
		seen[k] = i
	}
}

func reflogWith(h *harness, n int) (*tab, *gittest.Repo) {
	r := h.repoWith()
	for i := range n {
		r.Write("f.txt", fmt.Sprint(i))
		r.Commit(fmt.Sprint("commit ", i))
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("Reflog")
	h.app.Settings.NoSidebar = true
	h.until("the reflog", func() bool { return len(tb.reflog.entries) == n+1 })
	return tb, r
}

func TestReflogShowsNewEntriesAtItsTop(t *testing.T) {
	for _, scroll := range []int{0, 5} {
		t.Run(fmt.Sprint("scrolled ", scroll), func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetSize(1200, 400)
			tb, r := reflogWith(h, 30)
			h.rowsComeAbove(&tb.reflog.list.ListState, "Reflog entries", scroll, func() {
				r.Git("commit", "-q", "--allow-empty", "-m", "on top")
			}, func() bool { return len(tb.reflog.entries) == 32 })
			uniqueKeys(t, &tb.reflog.list.ListState, len(tb.reflog.entries))
		})
	}
}

func TestStashKeysAreUniqueWithAStashStoredTwice(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("README", "first")
	r.Git("stash", "push", "-q", "-m", "first")
	r.Write("README", "second")
	r.Git("stash", "push", "-q", "-m", "second")
	// The first stash again on top: two entries of one commit.
	r.Git("stash", "store", "-m", "first again", strings.TrimSpace(r.Git("rev-parse", "stash@{1}")))
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.app.Settings.NoSidebar = true
	tb.sel = "stashes"
	h.until("the stashes", func() bool { return len(tb.stash.entries) == 3 && hasExact(h, "first again") })
	uniqueKeys(t, &tb.stash.list.ListState, 3)
}

func TestStashesShowNewEntriesAtTheirTop(t *testing.T) {
	for _, scroll := range []int{0, 5} {
		t.Run(fmt.Sprint("scrolled ", scroll), func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetSize(1200, 400)
			r := h.repoWith()
			for i := range 30 {
				r.Write("README", fmt.Sprint(i))
				r.Git("stash", "push", "-q", "-m", fmt.Sprint("stash ", i))
			}
			h.trust.Set(r.GitDir(), true)
			h.app.Open(r.Dir)
			h.until("the repository", h.shows("No local changes"))
			tb := h.app.current()
			h.app.Settings.NoSidebar = true
			tb.sel = "stashes"
			h.until("the stashes", func() bool { return len(tb.stash.entries) == 30 })
			h.rowsComeAbove(&tb.stash.list.ListState, "Stash entries", scroll, func() {
				r.Write("README", "on top")
				r.Git("stash", "push", "-q", "-m", "on top")
			}, func() bool { return len(tb.stash.entries) == 31 })
			uniqueKeys(t, &tb.stash.list.ListState, len(tb.stash.entries))
		})
	}
}

// fileWith makes a repository whose f.txt n commits changed, each its
// second line.
func fileWith(h *harness, n int) *gittest.Repo {
	r := h.repoWith()
	for i := range n {
		r.Write("f.txt", fmt.Sprint("alpha\n", i, "\ngamma\n"))
		r.Commit(fmt.Sprint("change ", i))
	}
	h.trust.Set(r.GitDir(), true)
	return r
}

func TestFileHistoryShowsNewCommitsAtItsTop(t *testing.T) {
	for _, scroll := range []int{0, 5} {
		t.Run(fmt.Sprint("scrolled ", scroll), func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetSize(1200, 400)
			r := fileWith(h, 30)
			h.app.ShowFile(filepath.Join(r.Dir, "f.txt"), false)
			h.until("the history", func() bool { t := h.app.current(); return t != nil && len(t.file.commits) == 30 })
			tb := h.app.current()
			h.rowsComeAbove(&tb.file.list.ListState, "Commits of the file", scroll, func() {
				r.Write("f.txt", "on top\n")
				r.Commit("on top")
			}, func() bool { return len(tb.file.commits) == 31 })
			uniqueKeys(t, &tb.file.list.ListState, len(tb.file.commits))
		})
	}
}

func TestLineHistoryShowsNewCommitsAtItsTop(t *testing.T) {
	for _, scroll := range []int{0, 5} {
		t.Run(fmt.Sprint("scrolled ", scroll), func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetSize(1200, 400)
			r := fileWith(h, 30)
			h.app.ShowFile(filepath.Join(r.Dir, "f.txt"), true)
			h.until("the blame", func() bool { return hasExact(h, "29") && !h.tt.HasText("Blaming") })
			h.menu("29", "History of This Line")
			tb := h.app.current()
			h.until("the line history", func() bool { l := tb.file.lines.log; return l != nil && len(l.Commits) == 30 })
			h.rowsComeAbove(&tb.file.lines.list.ListState, "Commits that changed the lines", scroll, func() {
				r.Write("f.txt", "alpha\non top\ngamma\n")
				r.Commit("on top")
			}, func() bool { l := tb.file.lines.log; return l != nil && len(l.Commits) == 31 })
			uniqueKeys(t, &tb.file.lines.list.ListState, len(tb.file.lines.log.Commits))
		})
	}
}

func TestBranchPickerOpensAtItsTop(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	// Named before main, they are listed first until the branches' state
	// is read, which sorts the newest first; more than the picker shows.
	for i := range pickRows + 2 {
		r.Git("branch", fmt.Sprintf("a%02d", i))
	}
	r.Write("f.txt", "new")
	r.CommitAt("newest", 2_000_000_000)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.setSidebar(false)
	tb := h.app.current()
	// As before the branches' state is read, which the sidebar asked for
	// as the repository opened and may have by now.
	tb.refs.info, tb.refs.readFor = nil, nil
	h.click("Branch main")
	if tb.pick.info != nil || len(tb.pick.rows) == 0 || tb.pick.rows[0].ref != "refs/heads/a00" {
		t.Fatalf("the picker opened with its branches' state read: %v", tb.pick.rows)
	}
	h.until("the branches sorted", func() bool { return tb.pick.info != nil && hasExact(h, "main") })
	h.tt.Frame()
	if got := top(&tb.pick.list); got != 0 || tb.pick.rows[0].ref != "refs/heads/main" {
		t.Fatalf("the picker shows row %d of %v at its top, not the newest branch", got, tb.pick.rows)
	}
	uniqueKeys(t, &tb.pick.list, len(tb.pick.rows))
}

func TestBranchPickerStaysWhereScrolled(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for i := range 20 {
		r.Git("branch", fmt.Sprintf("b%02d", i))
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.setSidebar(false)
	tb := h.app.current()
	h.click("Branch main")
	h.until("the branches and their state", func() bool { return tb.pick.info != nil && hasExact(h, "b00") })
	h.rowsMoveUnder(&tb.pick.list, "Branches", 5, func() {
		r.Git("branch", "a-new")
	}, func() bool {
		// Sorted by its state, it comes first.
		_, ok := tb.pick.info["refs/heads/a-new"]
		return ok && len(tb.pick.rows) > 0 && tb.pick.rows[0].ref == "refs/heads/a-new"
	})
}

// A query typed in the branch picker scrolled down, the pointer off its
// rows, brings the row Enter takes into view.
func TestBranchPickerShowsTheRowEnterTakes(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for i := range 30 {
		r.Git("branch", fmt.Sprintf("a%02d", i))
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.setSidebar(false)
	tb := h.app.current()
	h.click("Branch main")
	h.until("the branches and their state", func() bool { return tb.pick.info != nil && hasExact(h, "a00") })
	h.scrollDown(&tb.pick.list, "Branches", 15)
	sf, _ := h.tt.Find("Search branches")
	h.tt.Move(sf.X+sf.W/2, sf.Y+sf.H/2)
	h.tt.Frame()
	// Every branch has an a: the rows stay as they are.
	h.tt.Type("a")
	h.until("the filter", func() bool { return tb.pick.rowsWith == "a" })
	h.tt.Frame()
	h.tt.Frame()
	first, last := tb.pick.list.Visible()
	if hi := tb.pick.hi; hi < first || hi > last {
		t.Fatalf("Enter would take row %d, %s, which the list (rows %d to %d) does not show", hi, tb.pick.rows[hi].ref, first, last)
	}
}

func TestRefsListStaysWhereScrolled(t *testing.T) {
	for _, scroll := range []int{0, 5} {
		t.Run(fmt.Sprint("scrolled ", scroll), func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetSize(1200, 400)
			r := h.repoWith()
			for i := range 30 {
				r.Git("branch", fmt.Sprintf("b%02d", i))
			}
			h.trust.Set(r.GitDir(), true)
			h.app.Open(r.Dir)
			h.until("the repository", h.shows("No local changes"))
			h.click("Branches and Tags")
			h.app.Settings.NoSidebar = true
			h.until("the branches", func() bool { return hasExact(h, "b00") })
			tb := h.app.current()
			h.rowsMoveUnder(&tb.refs.list.ListState, "Branches, tags and remotes", scroll, func() {
				r.Git("branch", "a-new")
			}, func() bool {
				return slices.ContainsFunc(tb.refs.rows, func(row refRow) bool { return row.label == "a-new" })
			})
			uniqueKeys(t, &tb.refs.list.ListState, len(tb.refs.rows))
		})
	}
}

func TestLocksShowNewLocksAtTheirTop(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("no git-lfs:", err)
	}
	for _, scroll := range []int{0, 5} {
		t.Run(fmt.Sprint("scrolled ", scroll), func(t *testing.T) {
			srv := lfsserver.New(t, "me")
			for i := range 30 {
				srv.Add(fmt.Sprintf("f%02d.psd", i), "alice")
			}
			h := newHarness(t)
			h.tt.SetSize(1200, 400)
			r := h.repoWith()
			r.Write(".gitattributes", "*.psd filter=lfs diff=lfs merge=lfs -text lockable\n")
			r.Commit("lfs")
			r.Git("config", "lfs.url", srv.URL)
			h.trust.Set(r.GitDir(), true)
			h.app.Open(r.Dir)
			h.until("the repository", h.shows("No local changes"))
			h.until("the locks in the sidebar", h.shows("LFS Locks"))
			h.click("LFS Locks")
			tb := h.app.current()
			h.until("the locks", func() bool { return len(tb.locks.locks.List) == 30 })
			h.rowsComeAbove(&tb.locks.list.ListState, "Locked files", scroll, func() {
				srv.Add("aaa.psd", "bob")
				tb.locks.listed = -1
			}, func() bool { return len(tb.locks.locks.List) == 31 })
			uniqueKeys(t, &tb.locks.list.ListState, len(tb.locks.locks.List))
		})
	}
}

func TestWorktreePanelKeepsItsPlace(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	aa := filepath.Join(base, "aa")
	r.Git("worktree", "add", "-q", "-b", "aa-work", aa)
	for i := range 20 {
		if err := os.WriteFile(filepath.Join(aa, fmt.Sprintf("f%02d.txt", i)), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the worktrees", func() bool { return len(tb.s.State.Worktrees) == 2 })
	h.click("Worktree Changes")
	h.until("aa's files", func() bool { return len(tb.wtp.sums[aa].Files) == 20 })
	h.click("Show the files changed in aa")
	h.until("aa open", h.shows("f00.txt"))
	// Only files come above its top: its first row is always the first
	// worktree.
	h.rowsComeAbove(&tb.wtp.list, "Worktrees and their changed files", 5, func() {
		if err := os.WriteFile(filepath.Join(aa, "a.txt"), []byte("2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}, func() bool { return len(tb.wtp.sums[aa].Files) == 21 })
	uniqueKeys(t, &tb.wtp.list, 2+21)
}

func TestWorktreeDashboardKeepsItsPlace(t *testing.T) {
	h := newHarness(t)
	h.tt.SetSize(1200, 300)
	r := h.repoWith()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	for i := range 12 {
		r.Git("worktree", "add", "-q", "-b", fmt.Sprintf("w%02d", i), filepath.Join(base, fmt.Sprintf("w%02d", i)))
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the worktrees", func() bool { return len(tb.s.State.Worktrees) == 13 })
	h.app.Settings.NoSidebar = true
	tb.sel = "worktrees"
	h.until("the dashboard", h.shows("13 worktrees"))
	// Its first row is always the main worktree: only a scrolled
	// dashboard can have rows come above its top, as git lists a new
	// worktree among the others.
	h.rowsComeAbove(&tb.dash.table.ListState, "Worktrees", 3, func() {
		r.Git("worktree", "add", "-q", "-b", "a-new", filepath.Join(base, "a-new"))
	}, func() bool { return len(tb.s.State.Worktrees) == 14 })
	uniqueKeys(t, &tb.dash.table.ListState, len(tb.s.State.Worktrees))
}
