// Package stats counts a history's commits by author and over time: by
// week of the commit time, the time ranges are chosen by as in git log
// --since, and by weekday and hour of the author's own clock, when the
// work was done.
package stats

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/mailmap"
	"ramus/internal/git/object"
)

// Author is one person's share of the commits, after the mailmap.
type Author struct {
	Name, Email string
	Commits     int
	// First and Last are the commit times of their first and last
	// commits, in seconds.
	First, Last int64
}

// Week is the commits of one week by commit time, which starts on a
// Monday of the committers' own calendars.
type Week struct {
	Start   time.Time // midnight UTC of that Monday
	Commits int
}

// Stats is what was counted.
type Stats struct {
	Commits int
	// Authors are sorted by commits, most first.
	Authors []Author
	// Weeks are in order, without the weeks no one committed in.
	Weeks []Week
	// Weekday counts by day of the author's week, Sunday first; Hour by
	// the hour of the author's day.
	Weekday [7]int
	Hour    [24]int
	// First and Last are the earliest and latest commit times.
	First, Last time.Time
}

// Counter adds commits up. The zero Counter is ready.
type Counter struct {
	mm      *mailmap.Map
	authors map[[2]string]*Author
	weeks   map[int64]int
	weekday [7]int
	hour    [24]int
	n       int
	first   int64
	last    int64
}

// NewCounter returns a counter that maps people through mm, which may be
// nil.
func NewCounter(mm *mailmap.Map) *Counter { return &Counter{mm: mm} }

// Add counts one commit by its author and committer.
func (c *Counter) Add(author, committer object.Signature) {
	if c.authors == nil {
		c.authors, c.weeks = map[[2]string]*Author{}, map[int64]int{}
	}
	when := committer.When
	name, email := c.mm.Lookup(string(author.Name), string(author.Email))
	key := [2]string{name, email}
	a := c.authors[key]
	if a == nil {
		a = &Author{Name: name, Email: email, First: when, Last: when}
		c.authors[key] = a
	}
	a.Commits++
	a.First, a.Last = min(a.First, when), max(a.Last, when)
	// Wall clocks: seconds since the epoch plus the zone's offset.
	aday, awd := day(author)
	c.weekday[awd]++
	c.hour[floorDiv(author.When+int64(author.Offset)*60-aday*86400, 3600)]++
	cday, cwd := day(committer)
	c.weeks[cday-int64((cwd+6)%7)]++
	if c.n == 0 || when < c.first {
		c.first = when
	}
	if c.n == 0 || when > c.last {
		c.last = when
	}
	c.n++
}

// day returns the day of a signature's own calendar, in days since the
// epoch, and its weekday, Sunday 0. 1970-01-01 was a Thursday.
func day(s object.Signature) (int64, int) {
	d := floorDiv(s.When+int64(s.Offset)*60, 86400)
	return d, int((d%7 + 7 + 4) % 7)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

// Merge adds o's counts to c.
func (c *Counter) Merge(o *Counter) {
	if o.n == 0 {
		return
	}
	if c.authors == nil {
		c.authors, c.weeks = map[[2]string]*Author{}, map[int64]int{}
	}
	for k, a := range o.authors {
		if b := c.authors[k]; b != nil {
			b.Commits += a.Commits
			b.First, b.Last = min(a.First, b.First), max(a.Last, b.Last)
		} else {
			cp := *a
			c.authors[k] = &cp
		}
	}
	for k, n := range o.weeks {
		c.weeks[k] += n
	}
	for i := range c.weekday {
		c.weekday[i] += o.weekday[i]
	}
	for i := range c.hour {
		c.hour[i] += o.hour[i]
	}
	if c.n == 0 || o.first < c.first {
		c.first = o.first
	}
	if c.n == 0 || o.last > c.last {
		c.last = o.last
	}
	c.n += o.n
}

// Stats returns the counts so far.
func (c *Counter) Stats() Stats {
	s := Stats{Commits: c.n, Weekday: c.weekday, Hour: c.hour}
	if c.n > 0 {
		s.First, s.Last = time.Unix(c.first, 0), time.Unix(c.last, 0)
	}
	for _, a := range c.authors {
		s.Authors = append(s.Authors, *a)
	}
	slices.SortFunc(s.Authors, func(a, b Author) int {
		return cmp.Or(cmp.Compare(b.Commits, a.Commits), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Email, b.Email))
	})
	for day, n := range c.weeks {
		s.Weeks = append(s.Weeks, Week{Start: time.Unix(day*86400, 0).UTC(), Commits: n})
	}
	slices.SortFunc(s.Weeks, func(a, b Week) int { return a.Start.Compare(b.Start) })
	return s
}

// Reader reads objects in batches, as a cat-file process does.
type Reader interface {
	Read(oids [][]byte, fn func(i int, o catfile.Object) error) error
}

// batch is the commits read in one cat-file round trip.
const batch = 2048

// Collect reads the commits ids names through readers, one goroutine
// each, counting their authors. progress, when set, gets the commits read
// and the counts so far every so often, from the collecting goroutine.
func Collect(ctx context.Context, readers []Reader, ids [][]byte, mm *mailmap.Map, progress func(read int, s Stats)) (Stats, error) {
	if len(readers) == 0 {
		return Stats{}, errors.New("stats: no reader")
	}
	work := make(chan [][]byte)
	type result struct {
		c   *Counter
		n   int
		err error
	}
	results := make(chan result)
	var wg sync.WaitGroup
	for _, rd := range readers {
		wg.Go(func() {
			for ids := range work {
				c := NewCounter(mm)
				err := rd.Read(ids, func(_ int, o catfile.Object) error {
					if o.Type != "commit" {
						return nil
					}
					cm, err := object.ParseCommit(o.Data)
					if err == nil {
						c.Add(cm.Author, cm.Committer)
					}
					return nil
				})
				results <- result{c, len(ids), err}
			}
		})
	}
	go func() {
		defer close(work)
		for i := 0; i < len(ids); i += batch {
			select {
			case work <- ids[i:min(i+batch, len(ids))]:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	total := NewCounter(mm)
	read, last := 0, time.Now()
	var first error
	for r := range results {
		if r.err != nil && first == nil {
			first = r.err
		}
		total.Merge(r.c)
		read += r.n
		if progress != nil && time.Since(last) > 250*time.Millisecond {
			last = time.Now()
			progress(read, total.Stats())
		}
	}
	if first == nil {
		first = ctx.Err()
	}
	return total.Stats(), first
}
