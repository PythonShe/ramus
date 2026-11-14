package run

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ramus/internal/git/redact"
)

// Record is one git process the runner started, as the command log shows
// it: its text made safe to show and its output cut short.
type Record struct {
	// GitDir is the repository it ran in; WorkTree for one run outside a
	// repository (clone, init).
	GitDir string
	// Line is the command in full; Short leaves out the options every
	// command gets (the repository, locks, encoding).
	Line, Short string
	Read        bool
	// Long marks a process that lives as long as its repository (cat-file
	// --batch-command): it is recorded when it starts, with no result.
	Long     bool
	Started  time.Time
	Duration time.Duration
	// Exit is the exit code, -1 when it did not exit on its own; Err says
	// why it failed, "" when it did not.
	Exit int
	Err  string
	// Stopped marks a command the app cancelled, as a view the user
	// left: not a failure.
	Stopped bool
	Stderr  string
	// Stdout is the start of what it printed; StdoutLen is all of it.
	Stdout    string
	StdoutLen int
}

// Bounds on what a journal keeps.
const (
	MaxRecords       = 2000
	MaxJournalBytes  = 4 << 20
	maxRecordedBytes = 4 << 10 // of each of stdout and stderr
)

// Journal keeps the latest commands a runner started, bounded in number
// and in bytes, for the command log. It is safe for concurrent use.
type Journal struct {
	mu    sync.Mutex
	recs  []Record // a ring once full
	next  int
	bytes int
	// version counts records added, so a reader can tell it is behind.
	version atomic.Uint64
}

func (r *Record) size() int {
	return len(r.GitDir) + len(r.Line) + len(r.Short) + len(r.Err) + len(r.Stderr) + len(r.Stdout) + 96
}

// add keeps rec, letting the oldest go past either bound.
func (j *Journal) add(rec Record) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.bytes += rec.size()
	if len(j.recs) < MaxRecords {
		j.recs = append(j.recs, rec)
	} else {
		j.bytes -= j.recs[j.next].size()
		j.recs[j.next] = rec
		j.next = (j.next + 1) % MaxRecords
	}
	for j.bytes > MaxJournalBytes && len(j.recs) > 1 {
		j.bytes -= j.recs[j.next].size()
		j.recs[j.next] = Record{}
		j.dropOldest()
	}
	j.version.Add(1)
}

// dropOldest removes the record at next, the oldest.
func (j *Journal) dropOldest() {
	ordered := append(append([]Record{}, j.recs[j.next+1:]...), j.recs[:j.next]...)
	j.recs, j.next = ordered, 0
}

// Version changes whenever a record is added.
func (j *Journal) Version() uint64 { return j.version.Load() }

// For returns the records of the repository at gitDir, oldest first.
func (j *Journal) For(gitDir string) []Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Record
	for i := range j.recs {
		if r := j.recs[(j.next+i)%len(j.recs)]; r.GitDir == gitDir {
			out = append(out, r)
		}
	}
	return out
}

// record notes a command that ended, or a long-lived one that started.
func (j *Journal) record(c *Cmd, line string, started time.Time, res Result, err error, long bool) {
	if j == nil {
		return
	}
	rec := Record{GitDir: c.GitDir, Line: redact.Shown(line), Short: redact.Shown("git " + strings.Join(c.Args, " ")), Read: c.Read, Long: long, Started: started,
		Duration: res.Duration, Stdout: head(res.Stdout), StdoutLen: len(res.Stdout)}
	if rec.GitDir == "" {
		rec.GitDir = c.WorkTree
	}
	if c.Stdout != nil {
		rec.StdoutLen = -1 // it went to the caller, not through the runner
	}
	if len(res.Stderr) > maxRecordedBytes {
		res.Stderr = res.Stderr[len(res.Stderr)-maxRecordedBytes:] // the end says why it failed
	}
	rec.Stderr = redact.Shown(res.Stderr)
	if err != nil {
		rec.Exit, rec.Err = -1, redact.Shown(err.Error())
		if e, ok := err.(*Error); ok {
			rec.Exit = e.ExitCode
			rec.Err = redact.Shown(e.Err.Error())
		}
		rec.Stopped = errors.Is(err, context.Canceled)
	}
	j.add(rec)
}

// head is the start of out as text: NULs, which separate records in the
// output of -z, become newlines.
func head(out []byte) string {
	return redact.Shown(strings.ReplaceAll(string(out[:min(len(out), maxRecordedBytes)]), "\x00", "\n"))
}
