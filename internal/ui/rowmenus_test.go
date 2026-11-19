package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest/lfsserver"
)

// The menus of a list's rows are built again for the item chosen, in a
// later frame: these lists change their rows while a menu is open, and
// the item chosen must act on the row the menu was opened on, not on the
// one that came to its index meanwhile.

// rightClickThen opens the menu of row, lets change move the rows, and
// chooses item once moved reports it.
func (h *harness) rightClickThen(row string, change func(), moved func() bool, item ...string) {
	h.t.Helper()
	if err := h.tt.RightClick(row); err != nil {
		h.t.Fatal(err)
	}
	h.tt.Frame()
	change()
	h.until("the rows moved", moved)
	if err := h.tt.ChooseMenuItem(item...); err != nil {
		h.t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.tt.Frame()
}

func TestBranchMenuFollowsItsBranch(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "dd")
	r.Git("branch", "ff")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.click("Branches and Tags")
	h.app.Settings.NoSidebar = true
	h.until("the branches", func() bool { return hasExact(h, "dd") && hasExact(h, "ff") })
	tb := h.app.current()
	// A branch made elsewhere comes in above it.
	h.rightClickThen("ff", func() { r.Git("branch", "ee") }, func() bool {
		return slices.ContainsFunc(tb.refs.rows, func(row refRow) bool { return row.label == "ee" })
	}, "Delete…")
	if !strings.Contains(tb.refs.ask.title, "branch ff ") {
		t.Fatalf("Delete chosen on ff asks: %q", tb.refs.ask.title)
	}
}

func TestReflogMenuFollowsItsEntry(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, f := range []string{"a", "b"} {
		r.Write(f+".txt", f+"\n")
		r.Commit("add " + f)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.click("Reflog")
	h.app.Settings.NoSidebar = true
	h.until("the reflog", h.shows("HEAD@{1}"))
	want := strings.TrimSpace(r.Git("rev-parse", "HEAD@{1}"))
	// Each commit puts an entry on top, moving every other down.
	h.rightClickThen("HEAD@{1}", func() {
		r.Write("c.txt", "c\n")
		r.Commit("add c")
	}, func() bool { return len(tb.reflog.entries) == 4 }, "Check Out Commit")
	h.idle(tb, "the checkout")
	if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != want {
		t.Fatalf("checked out %s; the menu was on %s", got[:10], want[:10])
	}
}

func TestBranchPickerMenuFollowsItsBranch(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "dd")
	r.Git("branch", "ff")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.app.setSidebar(false)
	h.click("Branch main")
	tb := h.app.current()
	h.until("the branches and their state", func() bool {
		_, ok := tb.pick.info["refs/heads/ff"]
		return ok && hasExact(h, "ff")
	})
	// A branch made elsewhere is listed above it once its state is read.
	at := func(ref string) int {
		return slices.IndexFunc(tb.pick.rows, func(p pickRow) bool { return p.ref == ref })
	}
	h.rightClickThen("ff", func() { r.Git("branch", "ee") }, func() bool {
		return at("refs/heads/ee") >= 0 && at("refs/heads/ee") < at("refs/heads/ff")
	}, "Delete…")
	h.until("the question", h.shows("Delete branch ff at "))
}

func TestWorktreePanelMenuFollowsItsWorktree(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	aa, zz := filepath.Join(base, "aa"), filepath.Join(base, "zz")
	r.Git("worktree", "add", "-q", "-b", "aa-work", aa)
	r.Git("worktree", "add", "-q", "-b", "zz-work", zz)
	if err := os.WriteFile(filepath.Join(aa, "one.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the worktrees", func() bool { return len(tb.s.State.Worktrees) == 3 })
	h.click("Worktree Changes")
	h.until("aa's file", func() bool { return len(tb.wtp.sums[aa].Files) == 1 })
	h.click("Show the files changed in aa")
	h.until("aa open", h.shows("one.txt"))
	// A file written in aa, opened above, moves zz down a row.
	h.rightClickThen("Show the files changed in zz", func() {
		if err := os.WriteFile(filepath.Join(aa, "two.txt"), []byte("2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}, h.shows("two.txt"), "Copy Path")
	if h.tt.Clipboard() != zz {
		t.Fatalf("Copy Path on zz copied %q", h.tt.Clipboard())
	}
}

func TestLineHistoryMenuFollowsItsCommit(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("f.txt", "alpha\nbeta\ngamma\n")
	r.Commit("add f")
	r.Write("f.txt", "alpha\nBETA\ngamma\n")
	want := r.Commit("shout beta")
	h.trust.Set(r.GitDir(), true)
	h.app.ShowFile(filepath.Join(r.Dir, "f.txt"), true)
	h.until("the blame", func() bool { return hasExact(h, "BETA") && !h.tt.HasText("Blaming") })
	h.menu("BETA", "History of This Line")
	h.until("the line history", func() bool { return hasExact(h, "shout beta") && hasExact(h, "add f") })
	tb := h.app.current()
	// A commit changing the line comes in on top.
	h.rightClickThen("shout beta", func() {
		r.Write("f.txt", "alpha\nBeta\ngamma\n")
		r.Commit("calm beta")
	}, func() bool { l := tb.file.lines.log; return l != nil && len(l.Commits) == 3 }, "Copy Hash")
	if h.tt.Clipboard() != want {
		t.Fatalf("Copy Hash on shout beta copied %q, not %q", h.tt.Clipboard(), want)
	}
}

func TestLockMenuFollowsItsLock(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("no git-lfs:", err)
	}
	srv := lfsserver.New(t, "me")
	srv.Add("theirs.psd", "alice")
	h := newHarness(t)
	r := h.repoWith()
	r.Write(".gitattributes", "*.psd filter=lfs diff=lfs merge=lfs -text lockable\n")
	r.Commit("lfs")
	r.Git("config", "lfs.url", srv.URL)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	h.until("the locks in the sidebar", h.shows("LFS Locks"))
	h.click("LFS Locks")
	h.until("their lock", h.shows("theirs.psd"))
	tb := h.app.current()
	// A lock taken elsewhere, listed first, comes in as the locks are read
	// again.
	h.rightClickThen("theirs.psd", func() {
		srv.Add("aaa.psd", "bob")
		tb.locks.listed = -1
	}, h.shows("aaa.psd"), "Force Unlock…")
	h.until("the question", h.shows("Force unlock “theirs.psd”?"))
}

func TestStashMenuFollowsItsEntry(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("README", "first")
	r.Git("stash", "push", "-q", "-m", "first")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	h.app.Settings.NoSidebar = true
	tb.sel = "stashes"
	h.until("the stash", h.shows("On main: first"))
	// A stash pushed elsewhere comes in on top.
	h.rightClickThen("On main: first", func() {
		r.Write("README", "second")
		r.Git("stash", "push", "-q", "-m", "second")
	}, func() bool { return len(tb.stash.entries) == 2 }, "Apply")
	h.idle(tb, "the apply")
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "README")); string(b) != "first" {
		t.Fatalf("Apply on first applied %q", b)
	}
}

func TestFileHistoryMenuFollowsItsCommit(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("f.txt", "1\n")
	want := r.Commit("add f")
	r.Write("f.txt", "2\n")
	r.Commit("change f")
	h.trust.Set(r.GitDir(), true)
	h.app.ShowFile(filepath.Join(r.Dir, "f.txt"), false)
	h.until("the history", func() bool { return hasExact(h, "add f") && hasExact(h, "change f") })
	tb := h.app.current()
	// A commit of the file comes in on top.
	h.rightClickThen("add f", func() {
		r.Write("f.txt", "3\n")
		r.Commit("change f again")
	}, func() bool { return len(tb.file.commits) == 3 }, "Copy Hash")
	if h.tt.Clipboard() != want {
		t.Fatalf("Copy Hash on add f copied %q, not %q", h.tt.Clipboard(), want)
	}
}

func TestManagerMenuFollowsItsRepository(t *testing.T) {
	h := newHarness(t)
	a, b, c := h.repoWith(), h.repoWith(), h.repoWith()
	h.lib.Opened(a.Dir, time.Unix(1_700_000_000, 0))
	h.lib.Opened(b.Dir, time.Unix(1_700_000_100, 0))
	h.tt.Frame()
	h.app.openManager()
	h.until("the manager", h.shows(b.Dir))
	// A repository opened since, listed first, comes in as the list is
	// read again.
	h.rightClickThen(b.Dir, func() {
		h.lib.Opened(c.Dir, time.Unix(1_700_000_200, 0))
		h.app.manager.dirty = true
	}, func() bool { return len(h.app.manager.repos) == 3 }, "Remove from This List")
	h.until("the library written", func() bool { return len(h.lib.Search("")) == 2 })
	for _, r := range h.lib.Search("") {
		if r.Path == b.Dir {
			t.Fatalf("Forget on b forgot another: %+v", h.lib.Search(""))
		}
	}
}
