package write

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

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

// onDisk is the work tree as a test compares it: the index, git's status,
// and every file, by its name as the disk spells it, with what it holds.
func (h *harness) onDisk() string {
	h.t.Helper()
	var b strings.Builder
	b.WriteString(h.r.Git("ls-files", "--stage", "-z"))
	b.WriteString("\n")
	b.WriteString(h.r.Git("status", "--porcelain", "-z", "-uall", "--ignored"))
	b.WriteString("\n")
	err := filepath.WalkDir(h.r.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(h.r.Dir, p)
		if d.Type().IsRegular() {
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "%q %q\n", filepath.ToSlash(rel), body)
		} else {
			fmt.Fprintf(&b, "%q %v\n", filepath.ToSlash(rel), d.Type())
		}
		return nil
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return b.String()
}

// likeGit runs the write setup makes on the repository it sets up, and
// git's own command for it, the arguments setup gives, on a second one
// made the same way: the two end alike. Undo then puts the first back as
// it was.
func likeGit(t *testing.T, setup func(h *harness) (Op, []string, error)) *harness {
	t.Helper()
	h, g := newHarness(t), newHarness(t)
	op, _, err := setup(h)
	_, args, gerr := setup(g)
	if err != nil || gerr != nil {
		t.Fatal(err, gerr)
	}
	before := h.onDisk()
	if g.onDisk() != before {
		t.Fatal("the two repositories differ before the write")
	}
	h.do(op, nil, true)
	g.r.Git(args...)
	if got, want := h.onDisk(), g.onDisk(); got != want {
		t.Fatalf("%s left\n%s\ngit leaves\n%s", op.Name, got, want)
	}
	h.undo()
	// git writes a file back under the index's name, composed, as its own
	// restore just did: the name may change form, never what it holds.
	if got := h.onDisk(); norm.NFC.String(got) != norm.NFC.String(before) {
		t.Fatalf("undo of %s left\n%s\nwant\n%s", op.Name, got, before)
	}
	return h
}

// A name the disk holds decomposed, as Finder and Cocoa apps make one, is
// the name git lists composed with core.precomposeunicode, which git
// init sets on such a volume: a discard and a restore from a commit under
// it end as git's own, and their undo puts the edit back.
func TestUndoUnderANameTheDiskHoldsDecomposed(t *testing.T) {
	nfdDir, nfcDir := "Übersicht", "Übersicht"
	nfdFile, nfcFile := "café.txt", "café.txt"
	probe := newHarness(t)
	if err := os.WriteFile(filepath.Join(probe.r.Dir, nfdFile), nil, 0o644); err != nil {
		t.Skip(err)
	}
	if _, err := os.Lstat(filepath.Join(probe.r.Dir, nfcFile)); err != nil {
		t.Skip("this volume tells the forms apart")
	}
	if strings.TrimSpace(probe.r.Git("config", "--type=bool", "core.precomposeunicode")) != "true" {
		t.Skip("git does not compose the names here")
	}
	setup := func(h *harness) string {
		if err := os.Mkdir(filepath.Join(h.r.Dir, nfdDir), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{nfdDir + "/a.txt", nfdFile} {
			if err := os.WriteFile(filepath.Join(h.r.Dir, p), []byte("one\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		h.r.Git("add", "-A")
		first := h.r.Commit("one")
		if got := h.r.Git("ls-files", "-z"); got != nfcFile+"\x00"+nfcDir+"/a.txt\x00" {
			t.Fatalf("git lists %q", got)
		}
		return first
	}
	for _, p := range []string{nfcDir + "/a.txt", nfcFile} {
		t.Run("discard "+p, func(t *testing.T) {
			h := likeGit(t, func(h *harness) (Op, []string, error) {
				setup(h)
				h.r.Write(p, "my edit\n")
				return Discard(h.w, []string{p}), []string{"restore", "--worktree", "--", p}, nil
			})
			if h.read(p) != "my edit\n" {
				t.Fatalf("after undo %q", h.read(p))
			}
		})
	}
	for _, p := range []string{nfcDir + "/a.txt", nfcDir} {
		t.Run("restore "+p+" from a commit", func(t *testing.T) {
			likeGit(t, func(h *harness) (Op, []string, error) {
				first := setup(h)
				h.r.Write(nfcDir+"/a.txt", "two\n")
				h.r.Commit("two")
				h.r.Write(nfcDir+"/a.txt", "my edit\n")
				h.r.Write(nfcDir+"/new", "untracked\n")
				op, err := RestoreFrom(h.w, p, first)
				return op, []string{"restore", "--source=" + first, "--worktree", "--no-overlay", "--", p}, err
			})
		})
	}
}

// A tracked folder renamed in case only outside git, which git does not
// see where core.ignorecase is set, holds files git still finds by the
// index's name: a discard and a restore from a commit under it end as
// git's own, and their undo puts the edits back, an untracked file in the
// folder included.
func TestUndoUnderAFolderRenamedInCase(t *testing.T) {
	if !foldsCase(t, t.TempDir()) {
		t.Skip("this volume tells case apart")
	}
	setup := func(h *harness) string {
		h.r.Write("docs/a", "one\n")
		h.r.Write("docs/b", "one\n")
		first := h.r.Commit("one")
		h.r.Git("rm", "-q", "docs/b")
		h.r.Write("docs/a", "two\n")
		h.r.Commit("two")
		for _, mv := range [][2]string{{"docs", "tmp-docs"}, {"tmp-docs", "Docs"}} {
			if err := os.Rename(filepath.Join(h.r.Dir, mv[0]), filepath.Join(h.r.Dir, mv[1])); err != nil {
				t.Fatal(err)
			}
		}
		if st := h.status(); st != "" {
			t.Fatalf("status after the rename %q", st)
		}
		h.r.Write("Docs/a", "my edit\n")
		h.r.Write("Docs/b", "mine, untracked\n")
		return first
	}
	t.Run("discard", func(t *testing.T) {
		likeGit(t, func(h *harness) (Op, []string, error) {
			setup(h)
			return Discard(h.w, []string{"docs/a"}), []string{"restore", "--worktree", "--", "docs/a"}, nil
		})
	})
	t.Run("restore docs/a from a commit", func(t *testing.T) {
		likeGit(t, func(h *harness) (Op, []string, error) {
			first := setup(h)
			op, err := RestoreFrom(h.w, "docs/a", first)
			return op, []string{"restore", "--source=" + first, "--worktree", "--no-overlay", "--", "docs/a"}, err
		})
	})
	// git takes the untracked Docs/b for a file outside docs, and its own
	// restore of docs writes over it: refused, nothing changed.
	for _, p := range []string{"docs/b", "docs"} {
		t.Run("restore "+p+" from a commit", func(t *testing.T) {
			h := newHarness(t)
			first := setup(h)
			before := h.onDisk()
			op, err := RestoreFrom(h.w, p, first)
			if err != nil {
				t.Fatal(err)
			}
			_, err = wait(t, h.q.Submit(op))
			if se := (*snapshot.SpellingError)(nil); !errors.As(err, &se) {
				t.Fatalf("restore of %s: %v", p, err)
			}
			if h.onDisk() != before {
				t.Fatalf("a refused restore changed\n%s", h.onDisk())
			}
		})
	}
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
