package session

import (
	"context"
	"strings"
	"sync"
	"time"

	"ramus/internal/actions"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
)

// LogEntry is one command the app ran for the user: a write through git
// or one of the user's actions, with its output as it came.
type LogEntry struct {
	Name string
	// Command is the command line, for showing next to git's output.
	Command string
	Output  []string
	// Dropped counts the first lines let go to keep Output bounded.
	Dropped int
	Running bool
	Err     error
	Started time.Time
	Ended   time.Time
}

// Bounds on what the log keeps.
const (
	MaxLogEntries = 200
	MaxLogLines   = 5000
)

func (s *Session) addLog(name, command string) *LogEntry {
	e := &LogEntry{Name: name, Command: command, Running: true, Started: s.timeNow()}
	s.Log = append(s.Log, e)
	if len(s.Log) > MaxLogEntries {
		s.Log = append(s.Log[:0:0], s.Log[len(s.Log)-MaxLogEntries:]...)
	}
	return e
}

func (e *LogEntry) add(lines []string) {
	e.Output = append(e.Output, lines...)
	if over := len(e.Output) - MaxLogLines; over > 0 {
		e.Output = append(e.Output[:0:0], e.Output[over:]...)
		e.Dropped += over
	}
}

// batcher gathers lines from a goroutine and hands them to the main
// thread at most every flushEvery, so chatty output does not flood it.
type batcher struct {
	mu      sync.Mutex
	pending []string
	update  func(func())
	e       *LogEntry
	timer   *time.Timer
}

const flushEvery = 50 * time.Millisecond

func (b *batcher) line(l string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = append(b.pending, l)
	if b.timer == nil {
		b.timer = time.AfterFunc(flushEvery, b.flush)
	}
}

func (b *batcher) flush() {
	b.mu.Lock()
	lines := b.pending
	b.pending, b.timer = nil, nil
	b.mu.Unlock()
	if len(lines) > 0 {
		b.update(func() { b.e.add(lines) })
	}
}

// finish hands the last lines and the result over, in that order.
func (b *batcher) finish(err error, ended time.Time) {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	lines := b.pending
	b.pending = nil
	b.mu.Unlock()
	b.update(func() {
		b.e.add(lines)
		b.e.Running, b.e.Err, b.e.Ended = false, err, ended
	})
}

// follow logs a write job's output and result.
func (s *Session) follow(e *LogEntry, j *write.Job) {
	b := &batcher{update: s.update, e: e}
	go func() {
		for ev := range j.Events() {
			if ev.Percent < 0 || strings.Contains(ev.Line, "done") {
				b.line(ev.Line) // progress redraws are not kept, only their last state
			}
		}
		_, err := j.Result()
		b.finish(err, s.timeNow())
		s.Refresh(repo.Rescan)
	}()
}

// RunAction runs one of the user's actions in the repository, logged. It
// is called on the main thread.
func (s *Session) RunAction(name string, argv []string) *LogEntry {
	e := s.addLog(name, strings.Join(argv, " "))
	r := s.Repo
	if r == nil {
		e.Running, e.Err = false, context.Canceled
		return e
	}
	dir := r.WorkTree
	if dir == "" {
		dir = r.GitDir
	}
	b := &batcher{update: s.update, e: e}
	r.Life().Go(func(ctx context.Context) {
		err := actions.Run(ctx, argv, dir, b.line)
		b.finish(err, s.timeNow())
		s.Refresh(repo.Rescan)
	})
	return e
}
