package ui

import (
	"path/filepath"
	"testing"
)

// A folder inside a repository open in a tab shows that tab, as the
// repository's own folder does, rather than open it a second time; and
// the repository's folder shows a tab opened from one inside it.
func TestSubfolderShowsTheOpenRepository(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(h.closeAll)
	r := h.repoWith("src/a.txt")
	other := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.trust.Set(other.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", func() bool { c := h.app.current(); return c != nil && c.top != "" })
	root := h.app.current()
	h.app.Open(other.Dir)
	h.until("the other repository", func() bool { c := h.app.current(); return c != nil && c != root && c.top != "" })

	h.app.Open(filepath.Join(r.Dir, "src"))
	h.until("the repository's tab shown", func() bool { return len(h.app.tabs) == 2 && h.app.current() == root })

	// Opened from inside first, then at its folder.
	h.app.Close(0)
	h.until("one tab", func() bool { return len(h.app.tabs) == 1 })
	h.app.Open(filepath.Join(r.Dir, "src"))
	h.until("the folder's repository", func() bool { c := h.app.current(); return len(h.app.tabs) == 2 && c.top != "" })
	inside := h.app.current()
	h.app.active = 0
	h.app.Open(r.Dir)
	if len(h.app.tabs) != 2 || h.app.current() != inside {
		t.Fatalf("%d tabs, the one shown at %s", len(h.app.tabs), h.app.current().s.Path)
	}
}

// The same across windows: a folder inside a repository another window
// has open shows it there, and opens nothing here, whichever of the two
// was opened first.
func TestSubfolderShowsTheOtherWindow(t *testing.T) {
	for _, first := range []string{"root", "inside"} {
		t.Run(first, func(t *testing.T) {
			h1, h2 := newHarness(t), newHarness(t)
			t.Cleanup(h1.closeAll)
			t.Cleanup(h2.closeAll)
			r := h2.repoWith("src/a.txt")
			h1.trust.Set(r.GitDir(), true)
			h2.trust.Set(r.GitDir(), true)
			// As windows.go wires it.
			h1.app.ShowOpen = func(p string) bool {
				if h2.app.Has(p) {
					h2.app.Open(p)
					return true
				}
				return false
			}
			there, here := r.Dir, filepath.Join(r.Dir, "src")
			if first == "inside" {
				there, here = here, there
			}
			h2.app.Open(there)
			h2.until("the repository", func() bool { c := h2.app.current(); return c != nil && c.top != "" })
			t2 := h2.app.current()
			h2.app.Open(h2.repoWith().Dir)
			h2.until("another tab shown", func() bool { return h2.app.current() != t2 })

			h1.app.Open(here)
			h1.until("handed over", func() bool { return len(h1.app.tabs) == 0 })
			h2.until("the repository shown", func() bool { return h2.app.current() == t2 })
			if len(h2.app.tabs) != 2 {
				t.Fatalf("window 2 has %d tabs", len(h2.app.tabs))
			}
		})
	}
}
