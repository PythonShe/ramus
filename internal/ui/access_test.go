package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/repo"
	"ramus/internal/session"
)

// named are the roles a screen reader must have a name for.
var named = map[ui.Role]bool{
	ui.RoleButton: true, ui.RoleLink: true, ui.RoleCheckBox: true, ui.RoleRadio: true, ui.RoleSwitch: true,
	ui.RoleSlider: true, ui.RoleTextField: true, ui.RolePopUpButton: true, ui.RoleTab: true, ui.RoleMenuButton: true,
	ui.RoleToggleButton: true, ui.RoleComboBox: true, ui.RoleDisclosure: true, ui.RoleListItem: true, ui.RoleTreeItem: true,
	ui.RoleRow: true, ui.RoleImage: true, ui.RoleList: true, ui.RoleTable: true, ui.RoleTree: true, ui.RoleSplitter: true,
	ui.RoleStepper: true,
}

// unnamed fails the test for each control of the last frame that a
// screen reader would have no name for.
func unnamed(h *harness, where string) {
	h.t.Helper()
	for _, a := range h.tt.AccessAll() {
		if named[a.Role] && strings.TrimSpace(a.Name) == "" {
			h.t.Errorf("%s: an element of role %d at %v has no accessible name", where, a.Role, a.Bounds)
		}
	}
}

// settle pumps updates and frames for a while, for views that load in
// the background.
func (h *harness) settle() {
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		h.tt.Frame()
		select {
		case fn := <-h.updates:
			fn()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Every view names its controls for screen readers, lists give each row
// its position and words, and a merge in the history reads as the
// commits it merges.
func TestAccessibility(t *testing.T) {
	h := newHarness(t)
	h.tt.Frame()
	unnamed(h, "welcome")
	r := h.repoWith()
	r.Git("checkout", "-q", "-b", "side")
	r.Write("b.txt", "b\n")
	r.Commit("on side")
	r.Git("checkout", "-q", "main")
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	r.Git("tag", "v1")
	r.Write("st.txt", "s\n")
	r.Git("add", "st.txt")
	r.Write("a.txt", "beta\n")
	r.Write("new.txt", "n\n")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("a.txt"))
	h.click("a.txt")
	h.until("the patch", func() bool { return h.app.current().cv.diff.pv.d != nil })
	unnamed(h, "changes")
	if a, ok := h.tt.Access("staged added st.txt"); !ok || a.Role != ui.RoleListItem || a.PosInSet != 2 || a.SetSize != 5 {
		t.Errorf("the staged row reads as %+v, %v", a, ok)
	}

	tb := h.app.current()
	for _, sel := range []string{"history", "log", "snapshots", "reflog", "stats", "refs", "stashes"} {
		tb.sel = sel
		h.settle()
		unnamed(h, sel)
	}
	tb.sel = "history"
	h.until("the merge", h.shows("merge side"))
	parents := strings.Fields(r.Git("log", "-1", "--format=%p", "HEAD"))
	want := "merge of " + parents[0] + " and " + parents[1] + ", merge side"
	found := false
	for _, a := range h.tt.AccessAll() {
		if a.Role == ui.RoleListItem && strings.HasPrefix(a.Name, want) {
			found = strings.Contains(a.Name, "current branch main") && strings.Contains(a.Name, "tag v1")
		}
	}
	if !found {
		t.Errorf("no history row reads as %q with its branch and tag", want)
	}
	if _, ok := h.tt.AccessByRole(ui.RoleImage, "merge of "+parents[0]+" and "+parents[1]); !ok {
		t.Error("the merge's graph cell does not name what it merges")
	}
	h.click("merge side")
	h.until("the detail", h.shows("Parents"))
	unnamed(h, "commit detail")

	h.app.ShowFile(filepath.Join(r.Dir, "a.txt"), true)
	h.until("the blame", func() bool { return tb.file.path == "a.txt" })
	h.settle()
	unnamed(h, "blame")
	for _, open := range []struct {
		name string
		fn   func()
	}{{"settings", h.app.openSettings}, {"manager", h.app.openManager}, {"palette", func() { h.app.palette.open = true }}, {"clone", h.app.ShowClone}, {"keys", h.app.openKeys}, {"diagnostics", h.app.openDiagnostics},
		{"new worktree", func() { h.app.openAddWorktree(tb, "refs/heads/side") }},
		{"remove worktree", func() {
			h.app.worktreeRemoveUI = worktreeRemoval{open: true, t: tb, wt: repo.Worktree{Path: r.Dir + "-x", Branch: "refs/heads/side"}}
		}},
		{"lock worktree", func() { h.app.worktreeLockUI = worktreeLock{open: true, t: tb, path: r.Dir + "-x"} }},
		{"remote checkout", func() {
			h.app.remoteUI = remoteCheckout{open: true, t: tb, remote: "refs/remotes/origin/side", other: "origin-side",
				local: session.BranchInfo{Name: "refs/heads/side", Upstream: "refs/remotes/origin/side", Ahead: 1, Behind: 2}}
		}},
		{"elsewhere", func() { h.app.elsewhereUI = elsewhere{open: true, branch: "refs/heads/side", folder: r.Dir + "-x"} }}} {
		open.fn()
		h.settle()
		unnamed(h, open.name)
		h.tt.Key(0, ui.KeyEscape)
		h.tt.Frame()
	}
}
