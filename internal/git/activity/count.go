package activity

import (
	"strings"
	"time"
)

// Filter picks the commits counted.
type Filter struct {
	// Since and Until bound the commit time, in seconds, as [Since,
	// Until); zero is open.
	Since, Until int64
	// Mine marks, per identity, the user's own; nil counts everyone.
	Mine []bool
}

func (f Filter) keep(r *Records, i int) bool {
	t := r.Commit[i]
	if f.Since != 0 && t < f.Since || f.Until != 0 && t >= f.Until {
		return false
	}
	return f.Mine == nil || f.Mine[r.Who[i]]
}

// Totals are what a repository's comparison row shows.
type Totals struct {
	Commits, ActiveDays, Contributors int
	// First and Last are the commit times of the first and last commits
	// counted.
	First, Last int64
}

// Heat counts commits by weekday (Sunday 0) and hour of the author's own
// clock.
type Heat [7][24]int

// Days counts commits by day of the author's calendar, in days since
// 1970-01-01.
type Days map[int32]int

// localDay returns the day of commit i on the author's calendar and the
// seconds into it.
func (r *Records) localDay(i int) (int32, int64) {
	local := r.At[i] + int64(r.Zone[i])*60
	d := local / 86400
	if local%86400 < 0 {
		d--
	}
	return int32(d), local - d*86400
}

// Count counts the commits f keeps, adding them to heat and days when
// they are set, and returns the repository's totals.
func (r *Records) Count(f Filter, heat *Heat, days Days) Totals {
	var t Totals
	seenDay := map[int32]bool{}
	who := make([]bool, r.Identities())
	for i := range r.At {
		if !f.keep(r, i) {
			continue
		}
		d, secs := r.localDay(i)
		if heat != nil {
			heat[(int(d)%7+7+4)%7][secs/3600]++
		}
		if days != nil {
			days[d]++
		}
		seenDay[d] = true
		if !who[r.Who[i]] {
			who[r.Who[i]] = true
			t.Contributors++
		}
		if t.Commits == 0 || r.Commit[i] < t.First {
			t.First = r.Commit[i]
		}
		if t.Commits == 0 || r.Commit[i] > t.Last {
			t.Last = r.Commit[i]
		}
		t.Commits++
	}
	t.ActiveDays = len(seenDay)
	return t
}

// Span returns the commit times of the first and last commits.
func (r *Records) Span() (first, last int64) {
	for i, t := range r.Commit {
		if i == 0 || t < first {
			first = t
		}
		if i == 0 || t > last {
			last = t
		}
	}
	return first, last
}

// People adds the addresses of the authors of the commits f keeps to
// into, lower-cased, so people are counted once across repositories.
func (r *Records) People(f Filter, into map[string]bool) {
	seen := make([]bool, r.Identities())
	for i := range r.At {
		if f.keep(r, i) && !seen[r.Who[i]] {
			seen[r.Who[i]] = true
			_, email := r.Identity(r.Who[i])
			into[strings.ToLower(email)] = true
		}
	}
}

// Calendar is what a calendar of activity says beside its squares.
type Calendar struct {
	// Longest is the longest run of days with a commit, ending on
	// LongestEnd; Current the run ending today or yesterday.
	Longest, Current int
	LongestEnd       int32
	// Busiest is the day with the most commits, BusiestCommits of them.
	Busiest        int32
	BusiestCommits int
	// Month is the first day of the month with the most commits,
	// MonthCommits of them.
	Month        time.Time
	MonthCommits int
}

// Summarize finds the streaks and the busiest day and month in days;
// today is the day number of today.
func Summarize(days Days, today int32) Calendar {
	var c Calendar
	months := map[time.Time]int{}
	for d, n := range days {
		if n > c.BusiestCommits || n == c.BusiestCommits && d > c.Busiest {
			c.Busiest, c.BusiestCommits = d, n
		}
		day := Date(d)
		months[time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)] += n
		// A run is counted from its first day only.
		if days[d-1] > 0 {
			continue
		}
		end := d
		for days[end+1] > 0 {
			end++
		}
		if run := int(end-d) + 1; run > c.Longest || run == c.Longest && end > c.LongestEnd {
			c.Longest, c.LongestEnd = run, end
		}
		if end == today || end == today-1 {
			c.Current = int(end-d) + 1
		}
	}
	for m, n := range months {
		if n > c.MonthCommits || n == c.MonthCommits && m.After(c.Month) {
			c.Month, c.MonthCommits = m, n
		}
	}
	return c
}

// Date returns the calendar day of a day number, at midnight UTC.
func Date(d int32) time.Time { return time.Unix(int64(d)*86400, 0).UTC() }

// DayOf returns the day number of t's calendar day where t is.
func DayOf(t time.Time) int32 {
	_, off := t.Zone()
	local := t.Unix() + int64(off)
	d := local / 86400
	if local%86400 < 0 {
		d--
	}
	return int32(d)
}
