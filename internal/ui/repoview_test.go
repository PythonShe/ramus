package ui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Names the repository holds, a branch's, a worktree's and a file's, are
// shown without the bidirectional controls that would make them read as
// another's, in the sidebar and the statistics as in the branches view.
func TestNamesFromTheRepositoryShownClean(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	const evil = "fix‮txt.exe"
	r.Git("branch", evil)
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	r.Git("worktree", "add", "-q", filepath.Join(tmp, "wt"), "-b", "wt‮b")
	r.Write("a‮b.txt", strings.Repeat("x", 100))
	r.Commit("named")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the branch", h.shows("fixtxt.exe"))
	h.until("the worktree", h.shows("wt  wtb"))
	h.click("Statistics")
	h.app.current().stats.mode = 1
	h.until("the file", h.shows("ab.txt"))
	if i := slices.IndexFunc(h.tt.Texts(), func(s string) bool { return strings.ContainsRune(s, '‮') }); i >= 0 {
		t.Fatalf("shown raw: %q", h.tt.Texts()[i])
	}
}
