package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

// Tens of thousands of refs cost a frame what a few do: labels and the
// grouped list are built off the main thread, the list builds the rows on
// screen, and a folder of branches closes.
func TestManyRefs(t *testing.T) {
	h := newHarness(t)
	packed := func(branches, tags int) *gittest.Repo {
		r := h.repoWith()
		head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
		var b strings.Builder
		b.WriteString("# pack-refs with: peeled fully-peeled sorted \n")
		for i := range branches {
			fmt.Fprintf(&b, "%s refs/heads/feature/%05d\n", head, i)
		}
		for i := range tags {
			fmt.Fprintf(&b, "%s refs/tags/v%05d\n", head, i)
		}
		if err := os.WriteFile(filepath.Join(r.GitDir(), "packed-refs"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		h.trust.Set(r.GitDir(), true)
		return r
	}
	// frames opens r in a tab of its own and times a frame of its history
	// and of its branches, with refs refs in all, all but more of them
	// named in the row of its one commit.
	frames := func(r *gittest.Repo, refs int, more string, budget func(view int) time.Duration) (tb *tab, history, branches time.Duration) {
		was := h.app.current()
		h.app.Open(r.Dir)
		h.until("open", func() bool { return h.app.current() != was && h.tt.HasText("No local changes") })
		tb = h.app.current()
		if n := tb.s.State.Refs.Len(); n != refs {
			t.Fatalf("%d refs", n)
		}
		h.click("History")
		h.until("the labels", func() bool { return len(tb.hist.labels[string(tb.s.State.Head.OID)]) == refs })
		// A screen reader hears the refs the row shows and how many more.
		h.until("the row named", func() bool {
			for _, a := range h.tt.AccessAll() {
				if strings.Contains(a.Name, "more refs") {
					if !strings.Contains(a.Name, more) || strings.Count(a.Name, ", ") > 8 {
						t.Fatalf("the row reads %.200q", a.Name)
					}
					return true
				}
			}
			return false
		})
		history = h.frameTime(11, budget(0))
		h.click("Branches and Tags")
		h.until("the branches", func() bool { return len(tb.refs.rows) > 0 && hasExact(h, "feature/") })
		branches = h.frameTime(11, budget(1))
		return tb, history, branches
	}
	// A thousand refs first, enough to fill what the sidebar lists of
	// each kind: what their frames take on this machine, now, is what
	// those of thirty times as many are held to. A time alone would say
	// how busy the machine is.
	_, fewHistory, fewRefs := frames(packed(600, 400), 1001, "998 more refs", func(int) time.Duration { return 0 })
	allowed := func(few time.Duration) time.Duration { return max(50*time.Millisecond, 3*few) }
	tb, history, refs := frames(packed(20000, 10000), 30001, "29,998 more refs", func(view int) time.Duration {
		return allowed([]time.Duration{fewHistory, fewRefs}[view])
	})
	t.Logf("30,001 refs: history frame %v, branches frame %v; 1,001 refs: %v and %v",
		history.Round(10*time.Microsecond), refs.Round(10*time.Microsecond), fewHistory.Round(10*time.Microsecond), fewRefs.Round(10*time.Microsecond))
	if history > allowed(fewHistory) || refs > allowed(fewRefs) {
		t.Fatalf("frames take %v and %v, with a thousand refs %v and %v", history, refs, fewHistory, fewRefs)
	}
	rows := len(tb.refs.rows)
	h.click("Close folder")
	h.until("the folder closed", func() bool { return len(tb.refs.rows) < rows })
}
