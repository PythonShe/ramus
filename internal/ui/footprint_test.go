package ui

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/write"
)

// Using a repository leaves nothing of the app's own in it: no file in
// the work tree, and in the git directory only what git writes for the
// writes asked for and the snapshot and backup refs (JetBrains' .idea,
// GitKraken's caches).
func TestNothingWritten(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, f := range []string{"a", "b"} {
		r.Write(f+".txt", f+"\n")
		r.Commit("add " + f)
	}
	r.Git("gc", "-q")
	r.Write("a.txt", "a, changed\n")
	list := func(root string, skip string) []string {
		var out []string
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if d.IsDir() && rel == skip {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		return out
	}
	gitDir := r.GitDir()
	work, git := list(r.Dir, ".git"), list(gitDir, "")
	h.trust.Set(gitDir, true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("a.txt"))
	tb := h.app.current()
	h.click("a.txt")
	for _, section := range []string{"History", "Reflog", "Branches and Tags", "Statistics", "Command Log", "Snapshots"} {
		h.click(section)
		h.settle()
	}
	h.app.ShowFile(filepath.Join(r.Dir, "b.txt"), true)
	h.until("the blame", func() bool { return hasExact(h, "b") && !h.tt.HasText("Blaming") })

	// Writes that keep a snapshot and a backup.
	w := tb.s.Where()
	submit := func(op write.Op, err error) {
		t.Helper()
		if err == nil {
			_, err = tb.s.Submit(op)
		}
		if err != nil {
			t.Fatal(err)
		}
		h.idle(tb, op.Name)
		if l := tb.s.Log; l[len(l)-1].Err != nil {
			t.Fatalf("%s: %v", op.Name, l[len(l)-1].Err)
		}
	}
	submit(write.Discard(w, []string{"a.txt"}), nil)
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	parent := strings.TrimSpace(r.Git("rev-parse", "HEAD~1"))
	submit(write.Reword(w, write.Rewrite{Commit: head, Parent: parent, Git: tb.s.Repo.Runner.Git().Version}, "add b, reworded\n", tb.s.Seen()))

	if extra := added(work, list(r.Dir, ".git")); len(extra) > 0 {
		t.Fatalf("the app wrote into the work tree: %q", extra)
	}
	for _, p := range added(git, list(gitDir, "")) {
		ours := strings.HasPrefix(p, "refs/ramus/backup/") || strings.HasPrefix(p, "refs/worktree/ramus/snapshots/") ||
			strings.HasPrefix(p, "logs/refs/ramus/backup/") || strings.HasPrefix(p, "logs/refs/worktree/ramus/snapshots/")
		gits := strings.HasPrefix(p, "objects/") || strings.HasPrefix(p, "refs/heads/") || strings.HasPrefix(p, "logs/HEAD") || strings.HasPrefix(p, "logs/refs/heads/") ||
			slices.Contains([]string{"ORIG_HEAD", "COMMIT_EDITMSG", "packed-refs", "index", "AUTO_MERGE", "REBASE_HEAD"}, p)
		if !ours && !gits {
			t.Errorf("the app left %s in the git directory", p)
		}
	}
	if refs := r.Git("for-each-ref", "--format=%(refname)", "refs/ramus/backup/", "refs/worktree/ramus/snapshots/"); strings.Count(refs, "\n") < 2 {
		t.Fatalf("no snapshot or backup kept: %q", refs)
	}
}

// added is what now has that before did not.
func added(before, now []string) []string {
	var out []string
	for _, p := range now {
		if !slices.Contains(before, p) {
			out = append(out, p)
		}
	}
	return out
}
