package ui

import (
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/repo"
	"ramus/internal/git/write"
)

// submoduleDir is where the submodule at path, slash-separated, is
// checked out.
func submoduleDir(t *tab, path string) string {
	return filepath.Join(t.s.Path, filepath.FromSlash(path))
}

// submoduleLabel is how the sidebar lists m.
func submoduleLabel(m repo.Submodule) string {
	if !m.Present {
		return m.Path + "  not checked out"
	}
	return m.Path
}

// submodulesMenu is what can be done with every submodule at once, from
// the section's header.
func (a *App) submodulesMenu(t *tab) func(*ui.Menu, *func()) {
	return func(m *ui.Menu, act *func()) {
		ro := t.s.Queue == nil
		if m.Item("Update All Submodules").Disabled(ro).Chosen() {
			*act = func() { a.submit(t, write.SubmoduleUpdate(t.s.Where(), nil, true, true), nil) }
		}
		if m.Item("Sync All Submodule URLs").Disabled(ro).Chosen() {
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
		if m.Item("Open in New Tab").Disabled(!sm.Present).Chosen() {
			act = func() { a.Open(dir) }
		}
		if a.NewWindow != nil && m.Item("Open in New Window").Disabled(!sm.Present).Chosen() {
			act = func() { a.NewWindow([]string{dir}) }
		}
		m.Separator()
		label := "Update"
		if !sm.Present {
			label = "Check Out"
		}
		if m.Item(label).Disabled(ro).Chosen() {
			act = func() { a.submit(t, write.SubmoduleUpdate(t.s.Where(), paths, true, true), nil) }
		}
		if m.Item("Initialize").Disabled(ro).Chosen() {
			act = func() { a.submit(t, write.SubmoduleInit(t.s.Where(), paths), nil) }
		}
		if m.Item("Sync URL").Disabled(ro).Chosen() {
			act = func() { a.submit(t, write.SubmoduleSync(t.s.Where(), paths, true), nil) }
		}
		m.Separator()
		if m.Item("Copy Path").Chosen() {
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
			a.notice = "Submodule " + path + " is not checked out."
		default:
			a.confirm = confirmState{open: true, title: "Submodule " + path + " is not checked out",
				message: "Check Out clones it from its URL and checks out the commit this repository records, with its own submodules.",
				button:  "Check Out",
				run:     func() { a.submit(t, write.SubmoduleUpdate(t.s.Where(), []string{path}, true, true), nil) }}
		}
	}
}
