package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// The tab bar offers the worktree panel once there is a second
// worktree. It shows, live, what changed in each against its HEAD, and
// the patch of a file. A worktree being written in opens view-only until
// taken over, and one left alone opens as any other.
func TestWorktreePanel(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the repository", func() bool { return tb.s.State.Status != nil && len(tb.s.State.Worktrees) == 1 })
	button := func() bool {
		_, ok := h.tt.AccessByRole(ui.RoleButton, "Worktree Changes")
		return ok
	}
	if button() {
		t.Fatal("the panel is offered with one worktree")
	}

	agent := filepath.Join(base, "agent")
	r.Git("worktree", "add", "-q", "-b", "agent-work", agent)
	quiet := filepath.Join(base, "quiet")
	r.Git("worktree", "add", "-q", "-b", "quiet-work", quiet)
	then := time.Now().Add(-time.Hour)
	for _, dir := range []string{quiet, filepath.Join(r.GitDir(), "worktrees", "quiet")} {
		filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
			if err == nil {
				os.Chtimes(p, then, then)
			}
			return nil
		})
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(agent, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README", "hello\nmore\n")
	write("new.txt", "a\nb\nc\n")
	h.until("the button", button)

	h.click("Worktree Changes")
	h.until("the counts", func() bool { return hasExact(h, "2 files") && hasExact(h, "+5") && hasExact(h, "−1") })
	h.until("the agent at work", h.shows("Changing now"))
	if !h.tt.HasText("agent-work") || !h.tt.HasText("quiet-work") {
		t.Fatalf("no branches; texts %q", h.tt.Texts())
	}

	h.click("Show the files changed in agent")
	h.until("its files", func() bool { return hasExact(h, "new.txt") })
	// Its state is told by a letter and in words, not by a colour alone.
	if _, ok := h.tt.AccessByRole(ui.RoleButton, "added new.txt, 3 lines added, 0 deleted"); !ok || !hasExact(h, "A") {
		t.Fatalf("the file's state is not told; texts %q", h.tt.Texts())
	}
	h.click("new.txt")
	h.until("the patch", patchHas(&tb.wtp.pv, "c", ""))
	write("new.txt", "a\nb\nc\nlive\n")
	h.until("the patch read again", patchHas(&tb.wtp.pv, "live", ""))
	h.click("Back to Worktrees")
	h.until("the list again", func() bool { return tb.wtp.file == nil && hasExact(h, "README") })

	h.click("Open quiet in a tab")
	h.until("its tab", func() bool { return len(h.app.tabs) == 2 && h.app.tabs[1].s.State.Status != nil })
	if q := h.app.tabs[1]; q.s.Queue == nil || q.s.State.Held {
		t.Fatal("a worktree left alone opened view-only")
	}
	if tb.wtp.stop != nil {
		t.Fatal("the panel reads on with its tab hidden")
	}

	h.app.active = 0
	h.tt.Frame()
	h.click("Worktree Changes")
	h.until("the panel", h.shows("Changing now"))
	h.click("Open agent in a tab")
	h.until("its tab", func() bool { return len(h.app.tabs) == 3 && h.app.tabs[2].s.State.Status != nil })
	held := h.app.tabs[2]
	h.until("the banner", h.shows("Another program is working in this worktree."))
	if held.s.Queue != nil || !held.s.State.Held {
		t.Fatal("a worktree being written in opened writable")
	}
	if held.s.WriteCommitGraph() == nil {
		t.Fatal("a held tab wrote")
	}
	h.click("Take Over")
	h.until("taken over", func() bool { return held.s.Queue != nil && !held.s.State.Held && !h.tt.HasText("Take Over") })
}
