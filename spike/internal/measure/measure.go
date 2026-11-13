// Package measure summarises timings for the spike programs.
package measure

import (
	"fmt"
	"io"
	"slices"
	"time"
)

// Summary is the distribution of a set of durations.
type Summary struct {
	N                  int
	P50, P95, P99, Max time.Duration
	Over8ms, Over16ms  int
}

// Summarize sorts a copy of ds and reads its percentiles.
func Summarize(ds []time.Duration) Summary {
	s := slices.Clone(ds)
	slices.Sort(s)
	if len(s) == 0 {
		return Summary{}
	}
	at := func(q float64) time.Duration { return s[min(len(s)-1, int(q*float64(len(s))))] }
	sum := Summary{N: len(s), P50: at(0.5), P95: at(0.95), P99: at(0.99), Max: s[len(s)-1]}
	for _, d := range s {
		if d > 8*time.Millisecond+300*time.Microsecond {
			sum.Over8ms++
		}
		if d > 16*time.Millisecond+700*time.Microsecond {
			sum.Over16ms++
		}
	}
	return sum
}

// Print writes the summary on one line.
func (s Summary) Print(w io.Writer, name string) {
	fmt.Fprintf(w, "%s: n=%d p50=%v p95=%v p99=%v max=%v over8.3ms=%d over16.7ms=%d\n",
		name, s.N, s.P50.Round(10*time.Microsecond), s.P95.Round(10*time.Microsecond),
		s.P99.Round(10*time.Microsecond), s.Max.Round(10*time.Microsecond), s.Over8ms, s.Over16ms)
}
