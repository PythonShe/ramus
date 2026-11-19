package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// savePatchFile saves req as a patch in a new file and returns its path.
func savePatchFile(t *testing.T, s *Session, m mainThread, req PatchSave) (string, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "changes.patch")
	var err error
	done := false
	s.SavePatch(req, file, func(e error) { err, done = e, true })
	m.until(t, "the save", func() bool { return done })
	return file, err
}

// A saved patch gets the mode a new file gets, not 0600; saved over a
// file, it keeps that file's mode.
func TestSavedPatchMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	r := gittest.Init(t)
	r.Write("a", "a\n")
	c := r.Commit("one")
	s, m := openFor(t, r)
	dir := t.TempDir()
	probe, err := os.Create(filepath.Join(dir, "probe"))
	if err != nil {
		t.Fatal(err)
	}
	probe.Close()
	fi, _ := os.Stat(probe.Name())
	want := fi.Mode().Perm()
	save := func(file string) os.FileMode {
		t.Helper()
		done := false
		s.SavePatch(PatchSave{Commits: []string{c}}, file, func(err error) {
			if err != nil {
				t.Error(err)
			}
			done = true
		})
		m.until(t, "the save", func() bool { return done })
		fi, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	file := filepath.Join(dir, "one.patch")
	if got := save(file); got != want {
		t.Errorf("a new patch file has mode %v, a new file %v", got, want)
	}
	os.Chmod(file, 0o640)
	if got := save(file); got != 0o640 {
		t.Errorf("a patch saved over one of mode 0640 has mode %v", got)
	}
}

// A merge commit is refused as a patch: git format-patch skips merges,
// and would write another commit under the merge's name.
func TestSavePatchRefusesAMerge(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "a\n")
	r.Commit("one")
	r.Git("switch", "-q", "-c", "side")
	r.Write("s", "s\n")
	r.Commit("side")
	r.Git("switch", "-q", "main")
	r.Write("m", "m\n")
	work := r.Commit("main work")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	s, m := openFor(t, r)
	for _, list := range [][]string{{merge}, {work, merge}} {
		file, err := savePatchFile(t, s, m, PatchSave{Commits: list})
		if err == nil {
			b, _ := os.ReadFile(file)
			t.Errorf("%d commits with a merge saved:\n%s", len(list), b)
		}
	}
	if _, err := savePatchFile(t, s, m, PatchSave{Commits: []string{work}}); err != nil {
		t.Fatalf("a plain commit: %v", err)
	}
}

// A patch saved from the changes applies with git apply on a clean
// checkout of the same base, whatever the user's diff config says, and
// holds the untracked files picked and both sides of a staged rename.
func TestSavedPatchApplies(t *testing.T) {
	for _, trust := range []bool{true, false} {
		r := gittest.Init(t)
		r.Write(".gitattributes", "*.txt diff=up\n")
		r.Write("a.txt", "hello\n\nend\n")
		r.Write("old.txt", "old\n")
		r.Commit("one")
		for k, v := range map[string]string{
			"diff.noprefix":           "true",
			"diff.mnemonicPrefix":     "true",
			"diff.relative":           "true",
			"diff.up.textconv":        "tr a-z A-Z <",
			"diff.suppressBlankEmpty": "true",
		} {
			r.Git("config", k, v)
		}
		r.Write("a.txt", "hello\n\nworld\nend\n")
		r.Write("new.txt", "new\n")
		r.Write("dir/n2.txt", "n2\n")
		r.Git("mv", "old.txt", "moved.txt")
		var dirs []string
		if trust {
			dirs = append(dirs, r.GitDir())
		}
		m := make(mainThread, 64)
		s := Open(testrun.Runner(t), trusted(t, dirs...), r.Dir, m.update)
		t.Cleanup(func() { s.Close(); <-s.Done() })
		m.until(t, "the status", func() bool { return s.State.Status != nil })
		before := r.Git("status", "--porcelain")
		unstaged, err := savePatchFile(t, s, m, PatchSave{Paths: []string{"a.txt", "new.txt", "dir/n2.txt"}})
		if err != nil {
			t.Fatal(err)
		}
		staged, err := savePatchFile(t, s, m, PatchSave{Paths: []string{"moved.txt", "old.txt"}, Cached: true})
		if err != nil {
			t.Fatal(err)
		}
		if after := r.Git("status", "--porcelain"); after != before {
			t.Fatalf("saving the patch changed the status from\n%s to\n%s", before, after)
		}
		r.Git("reset", "-q", "--hard")
		r.Git("clean", "-q", "-f", "-d")
		for _, p := range []string{unstaged, staged} {
			if _, err := r.Run(nil, "apply", p); err != nil {
				b, _ := os.ReadFile(p)
				t.Fatalf("trusted %v: the saved patch does not apply: %v\n%s", trust, err, b)
			}
		}
		for p, want := range map[string]string{"a.txt": "hello\n\nworld\nend\n", "new.txt": "new\n", "dir/n2.txt": "n2\n", "moved.txt": "old\n"} {
			if b, err := os.ReadFile(filepath.Join(r.Dir, p)); err != nil || string(b) != want {
				t.Errorf("trusted %v: %s after applying: %q, %v", trust, p, b, err)
			}
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "old.txt")); err == nil {
			t.Errorf("trusted %v: the rename's old path is still there", trust)
		}
	}
}
