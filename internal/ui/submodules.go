package ui

import (
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/repo"
	"ramus/internal/git/write"
	"ramus/locales"
)

// submoduleDir is where the submodule at path, slash-separated, is
// checked out.
func submoduleDir(t *tab, path string) string {
	return filepath.Join(t.s.Path, filepath.FromSlash(path))
}

// submoduleLabel is how the sidebar lists m.
func submoduleLabel(m repo.Submodule) string {
	if !m.Present {
		return locales.SubmoduleNotCheckedOutLabel(m.Path)
	}
	return m.Path
}

// submodulesMenu is what can be done with every submodule at once, from
// the section's header.
func (a *App) submodulesMenu(t *tab) func(*ui.Menu, *func()) {
	return func(m *ui.Menu, act *func()) {
		ro := t.s.Queue == nil
		if m.Item(locales.SubmoduleUpdateAll()).Disabled(ro).Chosen() {
			*act = func() { a.submit(t, write.SubmoduleUpdate(t.s.Where(), nil, true, true), nil) }
		}
		if m.Item(locales.SubmoduleSyncAll()).Disabled(ro).Chosen() {
			*act = func() { a.submit(t, write.SubmoduleSync(t.s.Where(), nil, true), nil) }
		}
		m.Separator()
	}
}

// submoduleMenu is what can be done with submodule sm from the sidebar.
func (a *App) submoduleMenu(it ui.Element, t *tab, sm repo.Submodule) {
	it.ContextMenu(func(m *ui.Menu) {
		ro := t.s.Queue == nil
		dir := submoduleDir(t, sm.Path)
		paths := []string{sm.Path}
		var act func()
		if m.Item(locales.SubmoduleOpenInNewTab()).Disabled(!sm.Present).Chosen() {
			act = func() { a.Open(dir) }
		}
		if a.NewWindow != nil && m.Item(locales.SubmoduleOpenInNewWindow()).Disabled(!sm.Present).Chosen() {
			act = func() { a.NewWindow([]string{dir}) }
		}
		m.Separator()
		label := locales.SubmoduleUpdate()
		if !sm.Present {
			label = locales.RefsCheckOut()
		}
		if m.Item(label).Disabled(ro).Chosen() {
			act = func() { a.submit(t, write.SubmoduleUpdate(t.s.Where(), paths, true, true), nil) }
		}
		if m.Item(locales.SubmoduleInitialize()).Disabled(ro).Chosen() {
			act = func() { a.submit(t, write.SubmoduleInit(t.s.Where(), paths), nil) }
		}
		if m.Item(locales.SubmoduleSyncURL()).Disabled(ro).Chosen() {
			act = func() { a.submit(t, write.SubmoduleSync(t.s.Where(), paths, true), nil) }
		}
		m.Separator()
		if m.Item(locales.SubmoduleCopyPath()).Chosen() {
			act = func() { a.clip = dir }
		}
		if act != nil {
			act()
		}
	})
}

// chooseSubmodule opens the submodule at path, or offers to check it out
// when it is not.
func (a *App) chooseSubmodule(t *tab, path string) {
	for _, sm := range t.s.State.Submodules {
		switch {
		case sm.Path != path:
		case sm.Present:
			a.Open(submoduleDir(t, path))
		case t.s.Queue == nil:
			a.notice = locales.SubmoduleNotCheckedOutNotice(path)
		default:
			a.confirm = confirmState{open: true, title: locales.SubmoduleNotCheckedOutTitle(path),
				message: locales.SubmoduleCheckOutMessage(),
				button:  locales.RefsCheckOut(),
				safe:    true,
				run:     func() { a.submit(t, write.SubmoduleUpdate(t.s.Where(), []string{path}, true, true), nil) }}
		}
	}
}
