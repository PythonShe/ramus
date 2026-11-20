package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"ramus/internal/drafts"
	"ramus/internal/library"
)

// otherSpellings are other names of one folder: through a symbolic link,
// and, where the file system ignores case, in other letters. A test of
// one this machine cannot make skips.
var otherSpellings = []struct {
	name  string
	spell func(t *testing.T, dir string) string
}{
	{"link", func(t *testing.T, dir string) string {
		tmp, _ := filepath.EvalSymlinks(t.TempDir())
		link := filepath.Join(tmp, "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Skip(err)
		}
		return link
	}},
	{"case", func(t *testing.T, dir string) string {
		base := []rune(dir)
		for i, r := range base {
			if unicode.IsLower(r) {
				base[i] = unicode.ToUpper(r)
			} else {
				base[i] = unicode.ToLower(r)
			}
		}
		other := string(base)
		if other == dir {
			t.Skip("no letters to change in " + dir)
		}
		if _, err := os.Stat(other); err != nil {
			t.Skip("the file system keeps case apart")
		}
		return other
	}},
}

// One folder reached under two spellings is one repository: one trust
// decision, one draft, one tab and session, one entry in the library.
func TestOneFolderUnderTwoSpellings(t *testing.T) {
	for _, sp := range otherSpellings {
		t.Run(sp.name, func(t *testing.T) {
			h := newHarness(t)
			store := drafts.New(t.TempDir())
			h.app.Drafts = store
			r := h.repoWith()
			real := library.Clean(r.Dir)
			other := sp.spell(t, real)
			if library.Clean(other) != real {
				t.Fatalf("%s is keyed as %s, %s as %s", other, library.Clean(other), real, real)
			}
			// Trusted under the one, opened under the other.
			h.trust.Set(r.GitDir(), true)
			h.app.Open(other)
			h.until("the commit box", h.shows("Commit to main"))
			tb := h.app.current()
			if tb.s.State.Restricted {
				t.Fatal("trust given under one spelling is not the other's")
			}
			cs := &tb.commit
			h.until("the draft read", func() bool { return cs.draftRead })
			cs.subject = "typed under " + sp.name
			h.tt.Frame()
			h.until("the draft kept", func() bool { d, _ := store.Load(real); return d.Subject == cs.subject })
			h.closeAll()
			h.app.Close(0)

			// By the real path: the same draft.
			h.app.Open(real)
			h.until("the commit box again", h.shows("Commit to main"))
			tb = h.app.current()
			h.until("the draft read again", func() bool { return tb.commit.draftRead })
			if s := tb.commit.subject; s != "typed under "+sp.name {
				t.Fatalf("opened by its real path, the draft is %q", s)
			}

			// By the other again while open: one tab, one session.
			h.app.Open(other)
			h.until("one tab", func() bool { return len(h.app.tabs) == 1 })
			if h.app.current() != tb {
				t.Fatal("the tab opened first is not shown")
			}
			h.app.WaitWrites(60e9)
			n := 0
			for _, e := range h.lib.Recent(10) {
				if strings.EqualFold(e.Path, real) {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("%d entries of the library for %s: %+v", n, real, h.lib.Recent(10))
			}
		})
	}
}

// A draft an earlier version kept, under the path as the user gave it,
// is found, and moves to the tab's own key once kept again: it does not
// come back once committed.
func TestDraftOfAnEarlierVersionIsFound(t *testing.T) {
	for _, sp := range otherSpellings {
		t.Run(sp.name, func(t *testing.T) {
			h := newHarness(t)
			store := drafts.New(t.TempDir())
			h.app.Drafts = store
			r := h.repoWith()
			real := library.Clean(r.Dir)
			other := sp.spell(t, real)
			// Kept under the path as given: 0.6 resolved symbolic links but
			// kept the letters given, and the path as given was the key in
			// between.
			old := other
			store.Keep(old, drafts.Draft{Subject: "left by 0.6"})
			if err := store.Flush(); err != nil {
				t.Fatal(err)
			}
			h.trust.Set(r.GitDir(), true)
			h.app.Open(other)
			h.until("the commit box", h.shows("Commit to main"))
			cs := &h.app.current().commit
			h.until("the draft read", func() bool { return cs.draftRead })
			if cs.subject != "left by 0.6" {
				t.Fatalf("the draft is %q", cs.subject)
			}
			cs.subject = "left by 0.6, edited"
			h.tt.Frame()
			h.until("kept under the tab's key", func() bool { d, _ := store.Load(real); return d.Subject == cs.subject })
			h.until("gone from the old key", func() bool { d, _ := store.Load(old); return d.Empty() })
		})
	}
}
