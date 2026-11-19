package write

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ramus/internal/git/snapshot"
)

// foldsCase asks the volume of dir whether it tells letter case apart.
func foldsCase(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, "case-probe")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p)
	a, _ := os.Lstat(p)
	b, err := os.Lstat(filepath.Join(dir, "CASE-PROBE"))
	return err == nil && os.SameFile(a, b)
}

// Restoring README from a commit while the disk has readme, untracked or
// with an uncommitted edit after a rename in case only, is refused on a
// volume that does not tell case apart, which would write over readme
// with nothing keeping it; on one that does, README is restored beside
// it, and undo takes it away again.
func TestRestoreFromRefusesAFileInAnotherCase(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		h := newHarness(t)
		h.r.Write("README", "old readme\n")
		first := h.r.Commit("one")
		h.r.Git("rm", "-q", "README")
		if tracked {
			h.r.Write("readme", "committed\n")
			h.r.Commit("rename in case only")
		} else {
			h.r.Commit("two")
		}
		h.r.Write("readme", "mine\n")
		folds := foldsCase(t, h.r.Dir)
		op, err := RestoreFrom(h.w, "README", first)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wait(t, h.q.Submit(op))
		var se *snapshot.SpellingError
		if folds && !errors.As(err, &se) || !folds && err != nil {
			t.Fatalf("tracked %v, volume folds case %v: %v", tracked, folds, err)
		}
		if h.read("readme") != "mine\n" {
			t.Fatalf("tracked %v: readme %q", tracked, h.read("readme"))
		}
		if !folds {
			if h.read("README") != "old readme\n" {
				t.Fatal("README not restored beside readme")
			}
			h.undo()
			if h.exists("README") || h.read("readme") != "mine\n" {
				t.Fatalf("after undo: README there %v, readme %q", h.exists("README"), h.read("readme"))
			}
		}
	}
}
