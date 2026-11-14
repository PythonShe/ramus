package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// Two folders compare as the files that differ, each shown as a patch;
// closing the comparison tells whoever waits on it.
func TestCompareFolders(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	old, new := filepath.Join(root, "old"), filepath.Join(root, "new")
	for p, s := range map[string]string{
		"old/s/x": "1\n", "new/s/x": "2\n", "old/gone.txt": "g\n", "new/added.txt": "a\n", "old/same": "s\n", "new/same": "s\n",
	} {
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	closed := 0
	h.app.Compare(old, new, func() { closed++ })
	h.until("the listing", func() bool { return hasExact(h, "s/x") && hasExact(h, "gone.txt") && hasExact(h, "added.txt") })
	if hasExact(h, "same") || !h.tt.HasText("3 files differ") {
		t.Fatalf("listing: %q", h.tt.Texts())
	}
	cs := &h.app.compareUI
	t.Cleanup(func() {
		if t.Failed() && cs.patch.Parsed != nil {
			for i := range cs.patch.Parsed.Files {
				t.Logf("patch file %d: %q", i, cs.patch.Parsed.Path(i))
			}
		}
		if t.Failed() {
			t.Logf("patch error %v, key %+v", cs.patch.Err, cs.key)
		}
	})
	shown := func(want string) func() bool {
		return func() bool {
			p := cs.patch.Parsed
			return p != nil && len(p.Files) == 1 && filepath.Base(p.Path(0)) == want && cs.pv.d != nil
		}
	}
	h.until("the first patch", func() bool { return cs.patch.Parsed != nil && cs.pv.d != nil })
	h.click("s/x")
	h.until("the patch of s/x", shown("x"))
	h.click("Close")
	h.until("closed", func() bool { return closed == 1 && !cs.open })

	// Two files, without a listing.
	h.app.Compare(filepath.Join(old, "s", "x"), filepath.Join(new, "s", "x"), nil)
	h.until("the file patch", shown("x"))
	if cs.dir {
		t.Fatal("two files compared as folders")
	}
	h.app.Compare(old, filepath.Join(new, "same"), nil)
	h.until("the mismatch", h.shows("are not both files or both folders"))
}
