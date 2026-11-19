package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gittest"
	"ramus/internal/git/repo"
	"ramus/internal/session"
)

// fitNames are names of the kinds that push what holds them past its
// edge: long with places to break, long without, of wide letters, and
// of letters built of several code points. Each is a name git takes.
var fitNames = []struct{ kind, name string }{
	{"short", "login"},
	{"200 letters", ("topic/" + strings.Repeat("a-long-branch-name-", 11))[:200]},
	{"40 letters unbroken", "averyveryveryveryverylongbranchnamewitho"},
	{"wide letters", "功能/" + strings.Repeat("登录页面重新设计", 8)},
	{"marks and emoji", strings.Repeat("é̂\U0001F469\U0001F3FD‍\U0001F4BB", 12)},
}

// fitView is a dialog or a bar that shows a name the user or a remote
// chose, in a control or a text.
type fitView struct {
	view string
	// open shows it with name; close takes it away again.
	open  func(h *harness, tb *tab, name string)
	close func(h *harness, tb *tab)
	// dialog is a dialog, whose own box comes first in the access tree;
	// otherwise the box is the window's. A bar in a pane beside another
	// gives the pane, and the names of its controls, which with those
	// that show the name are the ones checked: the other pane's lie
	// outside it.
	dialog bool
	pane   func(all []ui.AccessInfo) (ui.Rect, bool)
	own    []string
}

// controlRoles are the roles of what a pointer clicks.
var controlRoles = map[ui.Role]bool{
	ui.RoleButton: true, ui.RoleLink: true, ui.RoleCheckBox: true, ui.RoleRadio: true, ui.RoleSwitch: true,
	ui.RoleSlider: true, ui.RoleTextField: true, ui.RolePopUpButton: true, ui.RoleTab: true,
	ui.RoleMenuButton: true, ui.RoleToggleButton: true, ui.RoleComboBox: true, ui.RoleDisclosure: true,
	ui.RoleMenuItem: true, ui.RoleMenuItemCheckBox: true, ui.RoleMenuItemRadio: true,
}

// misfits says what of all lies outside box, or is a control that shows
// name narrower than leastNamedW.
func misfits(all []ui.AccessInfo, box ui.Rect, name string) []string {
	const eps = 0.5
	var out []string
	for _, e := range all {
		b := e.Bounds
		if b.W == 0 && b.H == 0 {
			continue
		}
		short := e.Name
		if r := []rune(short); len(r) > 32 {
			short = string(r[:32]) + "..."
		}
		switch {
		case b.X < box.X-eps || b.Y < box.Y-eps || b.X+b.W > box.X+box.W+eps || b.Y+b.H > box.Y+box.H+eps:
			out = append(out, fmt.Sprintf("%v %q at %v, outside %v", e.Role, short, b, box))
		case controlRoles[e.Role] && shows(e.Name, name) && b.W < leastNamedW-eps:
			out = append(out, fmt.Sprintf("%v %q at %v, narrower than %v", e.Role, short, b, leastNamedW))
		}
	}
	return out
}

// Every dialog and bar that shows a name the user or a remote chose
// keeps each of its controls and texts inside its edges, whatever the
// name, a control wide enough to click: a long name is cut short or
// wraps, it does not push what is beside it out of reach.
func TestNamesStayInside(t *testing.T) {
	h, tb, views := fitHarness(t)
	for _, v := range views {
		for _, n := range fitNames {
			box, all := v.show(h, tb, n.name, ui.Rect{W: 1200, H: 800})
			if !shows(strings.Join(h.tt.Texts(), "\n"), n.name) && !shows(strings.Join(names(all), "\n"), n.name) {
				t.Errorf("%s, a name %s: the name shows nowhere: %q", v.view, n.kind, h.tt.Texts())
			}
			for _, m := range misfits(all, box, n.name) {
				t.Errorf("%s, a name %s: %s", v.view, n.kind, m)
			}
			v.close(h, tb)
			h.tt.Frame()
		}
	}
}

// show opens v with name and returns the box it must keep inside, the
// dialog's or window, and the access tree's elements but the dialog's
// own.
func (v fitView) show(h *harness, tb *tab, name string, window ui.Rect) (ui.Rect, []ui.AccessInfo) {
	h.t.Helper()
	v.open(h, tb, name)
	h.tt.Frame()
	h.tt.Frame()
	all := h.tt.AccessAll()
	if v.pane != nil {
		p, ok := v.pane(all)
		if !ok {
			h.t.Fatalf("%s: no pane", v.view)
		}
		var in []ui.AccessInfo
		for _, e := range all {
			if controlRoles[e.Role] && (shows(e.Name, name) || slices.Contains(v.own, e.Name)) {
				in = append(in, e)
			}
		}
		return p, in
	}
	if !v.dialog {
		return window, all
	}
	if len(all) == 0 {
		h.t.Fatalf("%s: no dialog", v.view)
	}
	return all[0].Bounds, all[1:]
}

// fitHarness opens a repository and returns the views that show a name,
// to be opened in its tab.
func fitHarness(t *testing.T) (*harness, *tab, []fitView) {
	h := newHarness(t)
	r := h.repoWith()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the repository", func() bool { return tb.s.State.Head.Target != "" })
	head := tb.s.State.Head.Target
	// Only the views that read the folder are given one that is there.
	folder := func(name string) string { return "/home/you/src/" + strings.ReplaceAll(name, "/", "-") }

	return h, tb, []fitView{
		{view: "remote checkout, opened before the details", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.remoteUI = remoteCheckout{open: true, t: tb, remote: "refs/remotes/origin/" + name, local: session.BranchInfo{Name: "refs/heads/" + name}, early: true, other: "origin-x"}
			},
			close: func(h *harness, _ *tab) { h.app.remoteUI.open = false }},
		{view: "remote checkout of a branch behind", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.remoteUI = remoteCheckout{open: true, t: tb, remote: "refs/remotes/origin/" + name, other: "origin-x",
					local: session.BranchInfo{Name: "refs/heads/" + name, Upstream: "refs/remotes/origin/" + name, Behind: 1}}
			},
			close: func(h *harness, _ *tab) { h.app.remoteUI.open = false }},
		{view: "remote checkout of a branch diverged", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.remoteUI = remoteCheckout{open: true, t: tb, remote: "refs/remotes/origin/" + name, other: "origin-x",
					local: session.BranchInfo{Name: "refs/heads/" + name, Upstream: "refs/remotes/origin/" + name, Ahead: 1, Behind: 1}}
			},
			close: func(h *harness, _ *tab) { h.app.remoteUI.open = false }},
		{view: "remote checkout from a remote of the name", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				remote := strings.ReplaceAll(name, "/", "-")
				h.app.remoteUI = remoteCheckout{open: true, t: tb, remote: "refs/remotes/" + remote + "/x", local: session.BranchInfo{Name: "refs/heads/x"}, early: true, other: "x-2"}
			},
			close: func(h *harness, _ *tab) { h.app.remoteUI.open = false }},
		{view: "branch checked out elsewhere", dialog: true,
			open: func(h *harness, _ *tab, name string) {
				h.app.elsewhereUI = elsewhere{open: true, branch: "refs/heads/" + name, folder: folder(name)}
			},
			close: func(h *harness, _ *tab) { h.app.elsewhereUI.open = false }},
		{view: "worktree removal", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.worktreeRemoveUI = worktreeRemoval{open: true, t: tb, wt: repo.Worktree{Path: folder(name), Branch: "refs/heads/" + name}}
			},
			close: func(h *harness, _ *tab) { h.app.worktreeRemoveUI.open = false }},
		{view: "worktree lock", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.worktreeLockUI = worktreeLock{open: true, t: tb, path: folder(name)}
			},
			close: func(h *harness, _ *tab) { h.app.worktreeLockUI.open = false }},
		{view: "new worktree", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.worktreeUI = worktreeDialog{open: true, t: tb, branch: "refs/heads/" + name, path: folder(name)}
			},
			close: func(h *harness, _ *tab) { h.app.worktreeUI.open = false }},
		{view: "new branch from a branch", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.branchUI = branchDialog{open: true, t: tb, commit: strings.Repeat("0", 40), from: "refs/heads/" + name}
			},
			close: func(h *harness, _ *tab) { h.app.branchUI.open = false }},
		{view: "branch delete", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.deleteBranches(tb, []session.BranchInfo{{Name: "refs/heads/" + name, Commit: strings.Repeat("1", 40), Worktree: folder(name)}})
			},
			close: func(h *harness, tb *tab) { tb.refs.ask.open = false }},
		{view: "branch rename", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				tb.refs.dlg = refDialog{open: true, title: "Rename " + name, labels: []string{"New name"}, values: []string{name}, button: "Rename"}
			},
			close: func(h *harness, tb *tab) { tb.refs.dlg.open = false }},
		{view: "switch with changes", dialog: true,
			open:  func(h *harness, tb *tab, name string) { tb.refs.switching = "refs/heads/" + name },
			close: func(h *harness, tb *tab) { tb.refs.switching = "" }},
		{view: "merge", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				h.app.mergeUI = mergeDialog{open: true, t: tb, what: "refs/heads/" + name, preview: &session.MergePreview{UpToDate: true}}
			},
			close: func(h *harness, _ *tab) { h.app.mergeUI.open = false }},
		{view: "repositories with a group", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				if err := h.app.Library.SetGroup(name, []string{tb.s.Path}); err != nil {
					h.t.Fatal(err)
				}
				h.app.openManager()
			},
			close: func(h *harness, tb *tab) {
				h.app.manager.open = false
				for _, g := range h.app.Library.Groups() {
					if err := h.app.Library.SetGroup(g.Name, nil); err != nil {
						h.t.Fatal(err)
					}
				}
			}},
		{view: "branch clean-up against a branch", dialog: true,
			open: func(h *harness, tb *tab, name string) {
				tb.sel = "refs"
				tb.refs.clean = cleanupState{open: true, base: "refs/heads/" + name}
			},
			close: func(h *harness, tb *tab) { tb.refs.clean.open = false; tb.sel = "" }},
		{view: "statistics of the repositories of a group", dialog: true,
			open: func(h *harness, _ *tab, name string) {
				s := &h.app.libStats
				s.open, s.scope = true, name
			},
			close: func(h *harness, _ *tab) { h.app.libStats.open = false; h.app.libStats.scope = "" }},
		{view: "nothing to commit, behind and ahead of the upstream",
			open: func(h *harness, tb *tab, name string) {
				tb.sel = ""
				h.until("the upstream read", func() bool { return !tb.sync.loading && tb.sync.trackOf == head })
				tb.sync.track = session.Track{Branch: head, Upstream: "refs/remotes/origin/" + name, Ahead: 1, Behind: 2}
			},
			close: func(h *harness, tb *tab) { tb.sync.track = session.Track{Branch: head} }},
		{view: "history filtered to a branch", pane: rowOf("Content", "Filter"),
			own: []string{"Author", "Message", "Path"},
			open: func(h *harness, tb *tab, name string) {
				tb.sel = "history"
				tb.hist.filter = historyFilter{open: true, only: "refs/heads/" + name}
			},
			close: func(h *harness, tb *tab) { tb.hist.filter = historyFilter{}; tb.sel = "" }},
		{view: "reflog of a branch",
			open: func(h *harness, tb *tab, name string) {
				tb.sel = "reflog"
				tb.reflog.ref = "refs/heads/" + name
			},
			close: func(h *harness, tb *tab) { tb.reflog.ref = "HEAD"; tb.sel = "" }},
		{view: "commit box and bar on a branch of the name",
			open: func(h *harness, tb *tab, name string) {
				tb.sel = ""
				r.Git("switch", "-q", "-c", name)
				r.Write("a.txt", name)
				h.until("the change on "+name, func() bool {
					st := tb.s.State
					return st.Head.Target == "refs/heads/"+name && st.Status != nil && st.Status.Len() > 0 && h.tt.HasText("Commit to")
				})
			},
			close: func(h *harness, tb *tab) {
				r.Git("checkout", "-q", "-f", "main")
				r.Git("clean", "-qfd")
				h.until("main again", func() bool {
					st := tb.s.State
					return st.Head.Target == head && st.Status != nil && st.Status.Len() == 0
				})
			}},
		{view: "tabs of a repository in a folder of the name",
			open: func(h *harness, tb *tab, name string) {
				dir := filepath.Join(tmp, strings.ReplaceAll(name, "/", "-"))
				if err := os.Mkdir(dir, 0o755); err != nil {
					h.t.Fatal(err)
				}
				o := gittest.Open(h.t, dir)
				o.Git("init", "-q", "-b", "main")
				h.trust.Set(o.GitDir(), true)
				h.app.Open(dir)
				ot := h.app.current()
				h.until("the second tab", func() bool { return ot.s.State.Head.Target != "" })
			},
			close: func(h *harness, tb *tab) {
				h.note()
				h.app.Close(h.app.active)
				h.until("one tab", func() bool { return len(h.app.tabs) == 1 && h.app.current() == tb })
			}},
		{view: "statistics of a branch",
			open: func(h *harness, tb *tab, name string) {
				tb.sel = "stats"
				tb.stats.branch = "refs/heads/" + name
			},
			close: func(h *harness, tb *tab) { tb.stats.branch = ""; tb.sel = "" }},
		{view: "settings with an editor chosen", dialog: true,
			open: func(h *harness, _ *tab, name string) {
				h.app.openSettings()
				h.app.settingsUI.pane = paneTools
				h.app.settingsUI.editor = []string{"/Applications/" + strings.ReplaceAll(name, "/", "-") + ".app"}
			},
			close: func(h *harness, _ *tab) {
				// Put in force as it is chosen, the editor would stay for
				// the views after.
				h.app.settingsUI.editor = nil
				h.tt.Frame()
				h.app.settingsUI.open, h.app.notice = false, ""
			}},
	}
}

// rowOf finds the pane a bar sits in by a row that always fits it: from
// the left of first to the right of last.
func rowOf(first, last string) func(all []ui.AccessInfo) (ui.Rect, bool) {
	return func(all []ui.AccessInfo) (ui.Rect, bool) {
		var l, r ui.Rect
		for _, e := range all {
			switch e.Name {
			case first:
				l = e.Bounds
			case last:
				r = e.Bounds
			}
		}
		if l.W == 0 || r.W == 0 {
			return ui.Rect{}, false
		}
		return ui.Rect{X: l.X, Y: 0, W: r.X + r.W - l.X, H: 1e6}, true
	}
}

// shows reports whether s holds name, or name as a folder's or a
// remote's name of it, its slashes dashes.
func shows(s, name string) bool {
	return strings.Contains(s, name) || strings.Contains(s, strings.ReplaceAll(name, "/", "-"))
}

// names are the accessible names of all.
func names(all []ui.AccessInfo) []string {
	var out []string
	for _, e := range all {
		out = append(out, e.Name)
	}
	return out
}
