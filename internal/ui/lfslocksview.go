package ui

import (
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/lfs"
	"ramus/internal/git/redact"
	"ramus/internal/session"
	"ramus/locales"
)

// lockState is a tab's Git LFS locks view: the files locked on the
// repository's LFS server, and who holds each.
type lockState struct {
	// listed is the session's Writes count the locks were read at, -1
	// for never: a lock or unlock is a write, so it reads them again.
	listed  int
	loading bool
	// read is set once the locks were read at all, if not lately.
	read   bool
	locks  lfs.Locks
	err    string
	list   tableState
	chosen int
	// path is the file the next lock is taken on.
	path string
	ask  confirmState
}

func newLockState() lockState { return lockState{listed: -1, chosen: -1} }

// sideLocks is the sidebar key of the locks view.
const sideLocks = "lfslocks"

// loadLocks reads the locks when the view has none or a write may have
// changed them.
func (a *App) loadLocks(t *tab) {
	ls := &t.locks
	if ls.loading || ls.listed == t.s.Writes || t.s.Repo == nil {
		return
	}
	ls.loading = true
	at := t.s.Writes
	t.s.LFSLocks(func(l lfs.Locks, err error) {
		ls.loading, ls.listed, ls.read = false, at, true
		ls.err = ""
		switch {
		case errors.Is(err, lfs.ErrNoLFS):
			ls.err = locales.LocksNoLFS()
		case errors.Is(err, session.ErrReadOnly):
			ls.err = locales.LocksReadOnly()
		case err != nil:
			ls.err = errText(err)
		}
		var keep string
		if ls.chosen >= 0 && ls.chosen < len(ls.locks.List) {
			keep = ls.locks.List[ls.chosen].ID
		}
		ls.locks, ls.chosen = l, -1
		for i, x := range l.List {
			if x.ID == keep {
				ls.chosen = i
			}
		}
	})
}

// lfsLocksView lists the locks and takes and releases them.
func (a *App) lfsLocksView(c *ui.Context, t *tab) {
	ls := &t.locks
	a.loadLocks(t)
	ro := t.s.Queue == nil
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.RepoSectionLocks()).Bold().Grow(1)
			in := ui.TextInput(c, &ls.path).Label(locales.LocksPath()).Placeholder(locales.LocksPath()).Width(260).Disabled(ro)
			path := strings.TrimSpace(ls.path)
			if ui.Button(c, locales.LocksLock()).Disabled(ro || path == "").Clicked() || in.Submitted() && !ro && path != "" {
				if _, err := t.s.LFSLock(path); err != nil {
					a.tell(t, errText(err))
				} else {
					ls.path = ""
				}
			}
			if ui.Button(c, locales.MenuRefresh()).Disabled(ls.loading).Clicked() {
				ls.listed = -1
			}
		})
		locks := ls.locks.List
		switch {
		case ls.err != "":
			errorMessage(c, ls.err)
			return
		case len(locks) == 0 && ls.loading:
			loading(c, "")
			return
		case len(locks) == 0:
			emptyState(c, locales.LocksNone())
			return
		}
		ls.list.Selected = &ls.chosen
		ls.list.Label = func(i int) string { return locales.LocksRow(locks[i].Path, lockOwner(locks[i])) }
		var act func()
		ls.list.CellMenu = func(m *ui.Menu, i, _ int) { a.lockMenu(m, t, locks[i], &act) }
		cols := []ui.TableColumn{
			{Title: locales.ColumnFile(), ID: "file", MinWidth: 120},
			{Title: locales.ColumnOwner(), ID: "owner", Width: 160, MinWidth: 60},
			dateColumn(),
		}
		now := a.Now()
		a.savedTable(c, "lfslocks", &ls.list, cols, len(locks), func(i, col int) {
			l := locks[i]
			k := ls.list.ink(c, i)
			switch col {
			case 0:
				ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
					fileIcon(c, l.Path, k)
					ui.Text(c, redact.Control(l.Path)).SingleLine()
				})
			case 1:
				o := ui.Text(c, lockOwner(l)).SingleLine()
				if !l.Ours {
					o.TextColor(k.Muted)
				}
			case 2:
				mutedCell(c, k, ago(now, l.At))
			}
		}).Label(locales.LocksList()).Grow(1)
		if act != nil {
			act()
		}
	})
	a.confirmView(c, &ls.ask)
}

// lockOwner names who holds l, the user as themselves.
func lockOwner(l lfs.Lock) string {
	if l.Ours {
		return locales.LocksYou(redact.Control(l.Owner))
	}
	return redact.Control(l.Owner)
}

// lockMenu is the context menu of a lock: release it, forced when
// someone else holds it, and what a changed file's menu offers.
func (a *App) lockMenu(m *ui.Menu, t *tab, l lfs.Lock, act *func()) {
	ro := t.s.Queue == nil
	if m.Item(locales.LocksUnlock()).Disabled(ro || t.locks.locks.Verified && !l.Ours).Chosen() {
		*act = func() { a.unlock(t, l, false) }
	}
	if m.Item(locales.LocksForceUnlock()).Disabled(ro).Danger().Chosen() {
		msg := locales.LocksForceMessageOurs()
		if !l.Ours {
			msg = locales.LocksForceMessage(redact.Control(l.Owner))
		}
		*act = func() {
			t.locks.ask = confirmState{open: true, title: locales.LocksForceTitle(redact.Control(l.Path)), message: msg,
				button: locales.LocksForceButton(), run: func() { a.unlock(t, l, true) }}
		}
	}
	m.Separator()
	a.fileMenu(m, t, l.Path, act)
}

func (a *App) unlock(t *tab, l lfs.Lock, force bool) {
	if _, err := t.s.LFSUnlock(l, force); err != nil {
		a.tell(t, errText(err))
	}
}

// fileLockMenu offers a changed file's lock in its menu, in a repository
// that keeps files in LFS: to take it, to release it when it is the
// user's, or who holds it.
func (a *App) fileLockMenu(m *ui.Menu, t *tab, path string, act *func()) {
	if !t.s.State.LFS {
		return
	}
	// Read here too: the locks page may never have been shown, and a
	// lock offered on a file already locked only fails at the server.
	a.loadLocks(t)
	if !t.locks.read {
		m.Item(locales.LocksReading()).Disabled(true)
		m.Separator()
		return
	}
	if t.locks.err != "" {
		// Not offered blind: a lock taken may be another's.
		m.Item(locales.LocksNotRead()).Disabled(true)
		m.Separator()
		return
	}
	ro := t.s.Queue == nil
	for _, l := range t.locks.locks.List {
		if l.Path != path {
			continue
		}
		if l.Ours || !t.locks.locks.Verified {
			if m.Item(locales.LocksUnlockFile()).Disabled(ro).Chosen() {
				*act = func() { a.unlock(t, l, false) }
			}
		} else {
			m.Item(locales.LocksLockedBy(redact.Control(l.Owner))).Disabled(true)
		}
		m.Separator()
		return
	}
	if m.Item(locales.LocksLockFile()).Disabled(ro).Chosen() {
		*act = func() {
			if _, err := t.s.LFSLock(path); err != nil {
				a.tell(t, errText(err))
			}
		}
	}
	m.Separator()
}
