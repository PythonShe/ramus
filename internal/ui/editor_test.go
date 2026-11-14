package ui

import (
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/tools"
)

// A repository's files open in the editor chosen for it, kept in the
// library; the default is the first editor found.
func TestEditorPerRepository(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	h.until("the tools", func() bool { return h.app.palette.found })
	var eds []tools.Tool
	for _, name := range []string{"Alpha", "Beta"} {
		tl, err := tools.Custom(name, tools.Editor, []string{gittest.Shell(t)})
		if err != nil {
			t.Fatal(err)
		}
		eds = append(eds, tl)
	}
	h.app.palette.tools = eds
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.until("the prefs", func() bool { return tb.prefs.loaded })
	name := func() string {
		ed, ok := h.app.editor(tb)
		if !ok {
			t.Fatal("no editor")
		}
		return ed.Name
	}
	if n := name(); n != "Alpha" {
		t.Fatalf("default %q", n)
	}

	h.menu(filepath.Base(r.Dir), "Open Files In", "Beta")
	if n := name(); n != "Beta" {
		t.Fatalf("chosen %q", n)
	}
	h.app.writes.Wait()
	if e := h.lib.Editor(r.Dir); e != "Beta" {
		t.Fatalf("library %q", e)
	}

	// A choice no longer installed falls back to the default.
	h.app.palette.tools = eds[:1]
	if n := name(); n != "Alpha" {
		t.Fatalf("uninstalled %q", n)
	}
	h.app.palette.tools = eds
	h.menu(filepath.Base(r.Dir), "Open Files In", "Default (Alpha)")
	h.app.writes.Wait()
	if e := h.lib.Editor(r.Dir); e != "" || name() != "Alpha" {
		t.Fatalf("library %q, editor %q", e, name())
	}
}
