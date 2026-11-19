package ui

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// Trust opens the repository again: a read a view started of the
// repository as it was is answered that it opened again. No view shows
// that as an error, nor a notice, nor that no repository is open while it
// opens; each asks again of the repository open now and shows its data.
func TestTrustMidReadShowsNoError(t *testing.T) {
	type tcase struct {
		name string
		// open takes the view as far as the read; start starts it.
		open, start func(h *harness, tb *tab)
		// shows reports the view showing what the read read.
		shows func(h *harness, tb *tab) bool
	}
	hexOf := func(r *gittest.Repo, rev string) []byte {
		b, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", rev)))
		return b
	}
	nothing := func(*harness, *tab) {}
	show := func(sel string) func(*harness, *tab) { return func(_ *harness, tb *tab) { tb.sel = sel } }
	var r *gittest.Repo
	cases := []tcase{
		{"history detail", func(h *harness, tb *tab) {
			h.click("History")
			h.until("the commits", h.shows("add alpha"))
		}, func(h *harness, _ *tab) { h.click("add alpha") }, func(h *harness, tb *tab) bool {
			d := tb.hist.detail.d
			return d != nil && d.Done && d.Err == nil && h.tt.HasText("1 file changed")
		}},
		{"file history", nothing, func(h *harness, tb *tab) { h.app.showFile(tb, "a.txt", false) }, func(_ *harness, tb *tab) bool {
			return len(tb.file.commits) == 1 && tb.file.logErr == ""
		}},
		{"blame", nothing, func(h *harness, tb *tab) { h.app.showFile(tb, "a.txt", true) }, func(_ *harness, tb *tab) bool {
			fs := &tb.file
			return fs.blameDone && fs.blameErr == "" && strings.Contains(string(fs.text), "alpha")
		}},
		{"stats", nothing, show("stats"), func(_ *harness, tb *tab) bool {
			res := tb.stats.res
			return res.Done && res.Err == nil && res.Stats.Commits == 2
		}},
		{"branches", nothing, show("refs"), func(_ *harness, tb *tab) bool {
			info := tb.refs.info
			return info != nil && info.Err == nil && len(info.Branches) == 2
		}},
		{"branch clean-up", func(h *harness, tb *tab) {
			tb.sel = "refs"
			h.until("the branches", func() bool { return tb.refs.info != nil && !tb.refs.loading })
		}, func(h *harness, tb *tab) { h.app.openCleanup(tb) }, func(_ *harness, tb *tab) bool {
			cs := &tb.refs.clean
			return cs.open && !cs.loading && cs.err == "" && len(cs.found) == 1
		}},
		{"reflog", nothing, show("reflog"), func(_ *harness, tb *tab) bool {
			return len(tb.reflog.entries) > 0 && tb.reflog.err == ""
		}},
		{"lost commits", func(h *harness, tb *tab) {
			tb.sel, tb.reflog.lostShown = "reflog", 1
			h.until("the search button", h.shows("Find Lost Commits"))
		}, func(h *harness, _ *tab) { h.click("Find Lost Commits") }, func(_ *harness, tb *tab) bool {
			rs := &tb.reflog
			return rs.lostSearched && !rs.lostLoading && rs.lostErr == ""
		}},
		{"stashes", nothing, show("stashes"), func(_ *harness, tb *tab) bool {
			return len(tb.stash.entries) == 1 && tb.stash.err == ""
		}},
		{"snapshots", nothing, show("snapshots"), func(h *harness, tb *tab) bool {
			return !tb.snaps.loading && tb.snaps.err == "" && h.tt.HasText("No snapshots")
		}},
		{"compare branches", nothing, func(h *harness, tb *tab) {
			h.app.compareBranches(tb, hexOf(r, "topic"), hexOf(r, "main"), "topic", "main", 0)
		}, func(_ *harness, tb *tab) bool {
			d := tb.cmp.d
			return tb.cmp.base != nil && d != nil && d.Done && d.Err == nil && d.Files != nil && d.Files.Len() == 1
		}},
		{"patch of a change", func(h *harness, tb *tab) {
			h.until("the change", h.shows("README"))
		}, func(h *harness, _ *tab) { h.click("README") }, func(_ *harness, tb *tab) bool {
			return patchHas(&tb.cv.diff.pv, "work in progress", "hello")()
		}},
		{"worktrees", nothing, show("worktrees"), func(_ *harness, tb *tab) bool {
			sums := tb.dash.sums
			if len(sums) != 2 {
				return false
			}
			for _, s := range sums {
				if s.Err != nil {
					return false
				}
			}
			return true
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			r = h.repoWith()
			r.Write("a.txt", "alpha\n")
			r.Commit("add alpha")
			r.Git("branch", "topic", "HEAD~1")
			r.Write("README", "stashed\n")
			r.Git("stash", "-q")
			r.Write("README", "work in progress\n")
			r.Git("worktree", "add", "-q", filepath.Join(t.TempDir(), "second"), "topic")
			h.app.Open(r.Dir)
			h.until("the repository, read-only", func() bool {
				return len(h.app.tabs) == 1 && h.app.current().s.State.Status != nil && h.tt.HasText("Read-only")
			})
			tb := h.app.current()
			c.open(h, tb)
			// The reopen is posted before anything the read hands over:
			// Trust posts it before its save is counted done.
			old := tb.s.Repo
			if err := tb.s.Trust(); err != nil {
				t.Fatal(err)
			}
			if !h.trust.Wait(20 * time.Second) {
				t.Fatal("the trust was not saved")
			}
			c.start(h, tb)
			h.tt.Frame()
			bad := func() string {
				for _, s := range append(h.tt.Texts(), h.app.notice) {
					if strings.Contains(s, "opened again while this was read") || strings.Contains(s, "repository is not open") {
						return s
					}
				}
				return ""
			}
			h.until("the data from the repository open again", func() bool {
				if s := bad(); s != "" {
					t.Fatalf("shown: %q", s)
				}
				return tb.s.Repo != nil && tb.s.Repo != old && !tb.s.State.Restricted && c.shows(h, tb)
			})
			// Answers of the closed repository still on their way land
			// nowhere either.
			for range 10 {
				h.tt.Frame()
				for len(h.updates) > 0 {
					(<-h.updates)()
				}
				if s := bad(); s != "" {
					t.Fatalf("shown after the data: %q", s)
				}
			}
		})
	}
}

// The worktree panel's watch ends with the repository Trust opens again:
// the panel watches the worktrees again, of the repository open now, and
// shows what changes there after the Trust.
func TestWorktreePanelWatchesAgainAfterTrust(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Cleanup(h.closeAll) // before the folder above goes
	aa := filepath.Join(base, "aa")
	r.Git("worktree", "add", "-q", "-b", "aa-work", aa)
	if err := os.WriteFile(filepath.Join(aa, "one.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.app.Open(r.Dir)
	tb := h.app.current()
	h.until("the worktrees", func() bool { return len(tb.s.State.Worktrees) == 2 && h.tt.HasText("Read-only") })
	h.click("Worktree Changes")
	h.until("aa's file", func() bool { return len(tb.wtp.sums[aa].Files) == 1 })
	old := tb.s.Repo
	if err := tb.s.Trust(); err != nil {
		t.Fatal(err)
	}
	if !h.trust.Wait(20 * time.Second) {
		t.Fatal("the trust was not saved")
	}
	h.until("the repository open again", func() bool {
		return tb.s.Repo != nil && tb.s.Repo != old && !tb.s.State.Restricted && tb.wtp.stop != nil
	})
	if err := os.WriteFile(filepath.Join(aa, "two.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.until("the file made after the Trust", func() bool { return len(tb.wtp.sums[aa].Files) == 2 })
}
