package ui

import (
	"strings"
	"testing"
	"time"

	"ramus/internal/git/stats"
)

// A commit dated far from the others, as a broken or hostile repository
// can hold, neither hangs the chart over time nor gives it more than
// maxBars bars; every commit is still counted in it.
func TestOverTimeBoundsFarDates(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	week := func(sec int64) stats.Week {
		return stats.Week{Start: weekStart(weekOf(time.Unix(sec, 0))), Commits: 1}
	}
	for _, far := range []int64{253402300799, 1 << 40, 1 << 50, 1 << 60, -(1 << 50), -(1 << 60)} {
		weeks := []stats.Week{week(1700000000), week(far)}
		if far < 0 {
			weeks[0], weeks[1] = weeks[1], weeks[0]
		}
		done := make(chan []bar, 1)
		go func() { _, bars := overTime(weeks, now); done <- bars }()
		select {
		case bars := <-done:
			n := 0
			for _, b := range bars {
				n += b.n
			}
			if len(bars) > maxBars || n != 2 {
				t.Errorf("a commit at %d: %d bars counting %d commits", far, len(bars), n)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("a commit at %d: the chart still counting after 5s", far)
		}
	}
}

// The spans a repository really has keep their periods: weeks, then
// months, quarters and years; years too many for maxBars are grouped,
// each bar naming its first and last year.
func TestOverTimePeriods(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	at := func(y int, m time.Month, d int) stats.Week {
		return stats.Week{Start: weekStart(weekOf(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))), Commits: 1}
	}
	for _, c := range []struct {
		weeks       []stats.Week
		title       string
		bars        int
		first, last string
	}{
		{[]stats.Week{at(2024, 1, 3), at(2024, 3, 6)}, "Commits per week", 10, "Week of 2024-01-01", "Week of 2024-03-04"},
		{[]stats.Week{at(2020, 1, 8), at(2024, 3, 6)}, "Commits per month", 51, "January 2020", "March 2024"},
		{[]stats.Week{at(2000, 1, 5), at(2024, 3, 6)}, "Commits per quarter", 97, "Q1 2000", "Q1 2024"},
		{[]stats.Week{at(1900, 1, 3), at(2024, 3, 6)}, "Commits per year", 63, "1900 to 1901", "2024"},
		// A date more than a year ahead counts a year ahead.
		{[]stats.Week{at(2025, 1, 8), at(9999, 1, 6)}, "Commits per month", 35, "January 2025", "November 2027"},
	} {
		title, bars := overTime(c.weeks, now)
		if title != c.title || len(bars) != c.bars || bars[0].label != c.first || bars[len(bars)-1].label != c.last ||
			bars[0].n != 1 || bars[len(bars)-1].n != 1 {
			var labels []string
			for _, b := range bars {
				labels = append(labels, b.label)
			}
			t.Errorf("%v: %q, %d bars %s", c.weeks, title, len(bars), strings.Join(labels, ", "))
		}
	}
}

// A treemap read for one request and built after the view was left and
// shown again for another is not taken for the other's: the files of the
// branch asked for show, not those of the branch asked for before.
func TestTreemapDropsAnEarlierRead(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "other")
	r.Write("main.txt", "main")
	r.Commit("main only")
	r.Git("checkout", "-q", "other")
	r.Write("other.txt", "other")
	r.Commit("other only")
	r.Git("checkout", "-q", "-")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	h.click("Statistics")
	tab := h.app.current()
	h.until("the counts", func() bool { return tab.stats.res.Done })
	ts := &tab.stats.sizes

	// The app's updates are held while HEAD's sizes are read, and the one
	// that would store the treemap built from them is kept back.
	orig := h.app.Update
	held := make(chan func(), 64)
	h.app.Update = func(fn func()) { held <- fn }
	tab.stats.mode = 1
	h.tt.Frame()
	if !ts.loading {
		t.Fatal("the sizes are not read")
	}
	var store func()
	deadline := time.Now().Add(20 * time.Second)
	for store == nil {
		select {
		case fn := <-h.updates:
			fn()
		case fn := <-held:
			saved := *ts
			fn()
			if ts.data != saved.data {
				*ts, store = saved, fn
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("HEAD's sizes were never read")
		}
	}
	h.app.Update = orig
	for len(held) > 0 {
		(<-held)()
	}

	// Left and shown again, for another branch.
	tab.sel = "history"
	h.tt.Frame()
	tab.sel = "stats"
	tab.stats.branch = "refs/heads/other"
	h.app.countStats(tab)
	h.tt.Frame()
	if !ts.loading || ts.For.Branch != "refs/heads/other" {
		t.Fatalf("other's sizes not asked for: %+v", ts.For)
	}
	store()
	h.until("other's sizes", func() bool { return ts.data != nil && !ts.loading })
	if paths := string(ts.data.pw.Paths); !strings.Contains(paths, "other.txt") || strings.Contains(paths, "main.txt") {
		t.Fatalf("other's treemap holds %q", strings.ReplaceAll(paths, "\x00", " "))
	}
}

// A count cut short, by Trust reopening the repository or by the view
// left, is counted again as it was asked, not from the dates as they are
// typed now and not submitted: half a date would leave the view reading
// for good, a whole one would be counted as if submitted.
func TestStatsCutShortAskedAgainAsAsked(t *testing.T) {
	for _, cut := range []string{"trust", "leave"} {
		t.Run(cut, func(t *testing.T) {
			h := newHarness(t)
			r := h.repoWith()
			r.Write("a.txt", "alpha\n")
			r.Commit("add alpha")
			if cut == "leave" {
				h.trust.Set(r.GitDir(), true)
			}
			h.app.Open(r.Dir)
			h.until("the repository", func() bool {
				return len(h.app.tabs) == 1 && h.app.current().s.State.Status != nil && h.app.current().s.State.Refs != nil
			})
			tb := h.app.current()
			ss := &tb.stats
			old := tb.s.Repo
			if cut == "trust" {
				if err := tb.s.Trust(); err != nil {
					t.Fatal(err)
				}
				if !h.trust.Wait(20 * time.Second) {
					t.Fatal("the trust was not saved")
				}
			}
			tb.sel = "stats"
			h.tt.Frame()
			if ss.asked == nil || !ss.loading {
				t.Fatal("nothing asked")
			}
			ss.from = "2026-" // being typed, not submitted
			if cut == "trust" {
				h.until("the repository open again", func() bool { return tb.s.Repo != old && tb.s.Repo != nil })
			} else {
				tb.sel = "changes"
				h.tt.Frame()
				tb.sel = "stats"
			}
			h.until("counted again", func() bool { return ss.asked != nil && ss.res.Done })
			if ss.err != "" || ss.res.Err != nil || ss.res.Stats.Commits != 2 || !ss.asked.Since.IsZero() {
				t.Fatalf("error %q, %v; %d commits since %v", ss.err, ss.res.Err, ss.res.Stats.Commits, ss.asked.Since)
			}
		})
	}
}

// The branch chosen in the statistics' menu is the one it was shown for,
// when a branch whose name reads alike, once cleaned for showing, comes
// in before it while the menu is open.
func TestStatsBranchMenuFollowsItsBranch(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("branch", "a‮b")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	h.click("Statistics")
	tab := h.app.current()
	h.until("the counts", func() bool { return tab.stats.res.Done })
	h.click("All Branches and Tags")
	h.tt.Frame()
	r.Git("branch", "ab")
	// Read with no frame drawn: the menu shown is still the one opened.
	deadline := time.After(20 * time.Second)
	for {
		if _, ok := tab.s.State.Refs.Find("refs/heads/ab"); ok {
			break
		}
		select {
		case fn := <-h.updates:
			fn()
		case <-deadline:
			t.Fatal("the branch made was never read")
		}
	}
	if err := h.tt.ChooseMenuItem("ab"); err != nil {
		t.Fatalf("menu %q: %v", h.tt.Menu(), err)
	}
	h.tt.Frame()
	if got := tab.stats.branch; got != "refs/heads/a‮b" {
		t.Fatalf("chose %q", got)
	}
}
