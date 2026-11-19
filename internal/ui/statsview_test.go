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
