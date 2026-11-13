package ui

import (
	"slices"
	"testing"
	"time"
)

func TestLibraryStatsView(t *testing.T) {
	h := newHarness(t)
	own := h.repoWith()
	own.Git("config", "user.email", "test@example.com")
	for range 3 {
		own.Write("a", time.Now().String())
		own.Commit("work")
	}
	other := h.repoWith()
	other.Env = []string{"GIT_AUTHOR_EMAIL=someone@example.com"}
	other.Write("README", "changed")
	other.Git("commit", "-q", "-a", "-m", "theirs")
	other.Env = nil
	now := time.Now()
	h.lib.Opened(own.Dir, now)
	h.lib.Opened(other.Dir, now.Add(-time.Hour))
	h.lib.SetGroup("Work", []string{other.Dir})
	t.Cleanup(func() {
		if l := h.app.libStats.loader; l != nil {
			l.Close()
		}
	})

	h.app.openLibStats()
	s := &h.app.libStats
	h.until("every repository", func() bool { return !s.loading && !s.summing && s.sum != nil })
	if s.sum.Commits != 6 || s.sum.Contributors != 2 || len(s.sum.Rows) != 2 {
		t.Fatalf("summary %+v", s.sum)
	}
	h.until("the comparison", h.shows("Library Statistics"))
	for _, want := range []string{"6", "Longest streak", "Busiest day", "Most active month"} {
		if !slices.Contains(h.tt.Texts(), want) {
			t.Fatalf("no %q in %q", want, h.tt.Texts())
		}
	}

	h.click("Only My Commits")
	h.until("my commits", func() bool { return !s.summing && s.sum.Commits == 5 })
	h.app.Settings.Emails = []string{"SOMEONE@example.com"}
	h.app.summarize()
	h.until("with an added address", func() bool { return !s.summing && s.sum.Commits == 6 })

	s.scope, s.mine = "Work", false
	h.app.loadLibStats()
	h.until("the group", func() bool { return !s.loading && !s.summing && len(s.sum.Rows) == 1 })
	if s.sum.Rows[0].Totals.Commits != 2 {
		t.Fatalf("group %+v", s.sum.Rows)
	}
	s.from = "2001-01-01"
	s.to = "2001-12-31"
	h.app.summarize()
	h.until("an empty range", func() bool { return !s.summing && s.sum.Commits == 0 })

	// Closing stops a read, and opening reads again.
	s.open = false
	h.tt.Frame()
	if s.loading {
		t.Fatal("still loading once closed")
	}
}
