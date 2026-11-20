package ui

import (
	"path/filepath"
	"slices"

	"ramus/internal/git/repo"
	"ramus/internal/library"
	"ramus/internal/settings"
)

// UseSettings takes settings another window changed; they take effect
// from the next frame, as when changed here.
func (a *App) UseSettings(s settings.Settings) {
	tools := !slices.Equal(s.Editor, a.Settings.Editor) || !slices.Equal(s.Terminal, a.Settings.Terminal)
	a.Settings = s
	a.settingsToSessions()
	if tools {
		a.findTools()
	}
	if a.settingsUI.open {
		a.loadSettingsForm()
	}
}

// settingsToSessions gives the open repositories what of the settings
// changes what they read.
func (a *App) settingsToSessions() {
	for _, t := range a.tabs {
		t.s.ShowDependencyFolders(a.Settings.ShowDependencyFolders)
	}
}

// moveToNewWindow closes tab i here and opens its repository in a window
// of its own.
func (a *App) moveToNewWindow(i int) {
	if a.NewWindow == nil || i < 0 || i >= len(a.tabs) {
		return
	}
	t := a.tabs[i]
	a.askToDrop(t, func() {
		if i := slices.Index(a.tabs, t); i >= 0 {
			a.Close(i)
			a.NewWindow([]string{t.s.Path})
		}
	})
}

// Has reports whether the repository at path is open in a tab here. It
// does not touch the disk: a path through a symbolic link is known once
// the tab's own path was resolved (resolveTab).
func (a *App) Has(path string) bool {
	path = cleanPath(path)
	return slices.ContainsFunc(a.tabs, func(t *tab) bool { return t.is(path) })
}

// cleanPath is path made absolute and clean without touching the disk:
// on the main thread a volume that stopped answering would hang the
// window. library.Clean, which resolves symbolic links, runs off it.
func cleanPath(p string) string {
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return filepath.Clean(p)
}

// is reports whether tab t has the repository at path, a clean path, as
// opened, as resolved, or as the top of the repository a folder in it
// opened.
func (t *tab) is(path string) bool {
	return t.s.Path == path || t.real == path || t.top == path
}

// key is tab t's path as the library keys it once resolved, the path it
// was opened with until then.
func (t *tab) key() string {
	if t.real != "" {
		return t.real
	}
	return t.s.Path
}

// resolveTab resolves the path tab t was opened with off the main thread,
// and finds the repository there. When it turns out to be a repository
// open already, under another name or from another of its folders, in
// this window or another, the new tab closes and that one shows instead,
// unless the user has a write running in it already.
func (a *App) resolveTab(t *tab) {
	path, update, resolve := t.s.Path, a.Update, a.resolve
	if resolve == nil {
		resolve = library.Clean
	}
	a.goWork(t, func() {
		real := resolve(path)
		top := repoTop(real)
		update(func() {
			t.real, t.top = real, top
			if (real == path && top == real) || !slices.Contains(a.tabs, t) || busyWrite(t) != nil || t.unsentPR() > 0 {
				return
			}
			same := func(o *tab) bool { return o != t && (o.is(real) || top != "" && o.is(top)) }
			// A file asked for may wait for this tab, opened at its folder:
			// it goes with the repository to the tab shown instead.
			f := a.pendingFile
			ours := f.abs != "" && t.is(f.dir)
			if i := slices.IndexFunc(a.tabs, same); i >= 0 {
				o, shown := a.tabs[i], a.current() == t
				if ours {
					a.pendingFile.dir = o.s.Path
				}
				a.Close(slices.Index(a.tabs, t))
				if shown {
					a.active = slices.Index(a.tabs, o)
				}
				return
			}
			if a.ShowOpen == nil {
				return
			}
			asks := []string{real}
			if top != real {
				asks = append(asks, top)
			}
			for _, p := range asks {
				// Open asked for path already.
				if p == "" || p == path {
					continue
				}
				if ours && a.ShowFileOpen != nil {
					if a.ShowFileOpen(p, f.abs, f.blame) {
						a.pendingFile = pendingFile{asked: f.asked}
						a.Close(slices.Index(a.tabs, t))
						return
					}
					continue
				}
				if a.ShowOpen(p) {
					a.Close(slices.Index(a.tabs, t))
					return
				}
			}
		})
	})
}

// repoTop is the top of the repository that has the folder path, its git
// directory when bare, "" when none does. It reads the disk.
func repoTop(path string) string {
	loc, err := repo.Discover(path)
	switch {
	case err != nil:
		return ""
	case loc.WorkTree != "":
		return filepath.Clean(loc.WorkTree)
	}
	return filepath.Clean(loc.GitDir)
}
