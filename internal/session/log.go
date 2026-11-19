package session

import (
	"context"
	"strings"
	"sync"
	"time"

	"ramus/internal/actions"
	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
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
	// Progress is the last line git printed while running, as
	// "Receiving objects:  42% (…)", Percent the share it reports, -1
	// none.
	Progress string
	Percent  int
	// Quiet is a write the app made on its own, which no one waits on:
	// it is not told of as failing, nor waited on to close.
	Quiet bool
	// job is the write running, for Cancel; nil for an action.
	job *write.Job
}

// Cancel stops the write while it runs; git and what it started end.
func (e *LogEntry) Cancel() {
	if e.Running && e.job != nil {
		e.job.Cancel()
	}
}

// Op is the write e logged, for submitting it again; false for an
// action, or a write the engine makes of several commands.
func (e *LogEntry) Op() (write.Op, bool) {
	if e.job == nil || !e.job.Op.Repeatable() {
		return write.Op{}, false
	}
	return e.job.Op, true
}

// Recorded is the History entry the write recorded once it ended, for
// UndoWrite; 0 for none.
func (e *LogEntry) Recorded() uint64 {
	if e.job == nil {
		return 0
	}
	return e.job.Entry()
}

// Cancellable reports a write that Cancel stops.
func (e *LogEntry) Cancellable() bool { return e.Running && e.job != nil }

// Bounds on what the log keeps.
const (
	MaxLogEntries = 200
	MaxLogLines   = 5000
)

func (s *Session) addLog(name, command string) *LogEntry {
	e := &LogEntry{Name: name, Command: redact.Shown(command), Running: true, Started: s.timeNow()}
	s.Log = append(s.Log, e)
	if len(s.Log) > MaxLogEntries {
		s.Log = append(s.Log[:0:0], s.Log[len(s.Log)-MaxLogEntries:]...)
	}
	return e
}

func (e *LogEntry) add(lines []string) {
	for i, l := range lines {
		// What a command prints may hold escape sequences that would
		// restyle or spoof the log, and secrets that must not stay.
		lines[i] = redact.Shown(l)
	}
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
	// progress is the last line seen, percent its share; moved is set
	// when either changed since the last flush.
	progress string
	percent  int
	moved    bool
	update   func(func())
	e        *LogEntry
	timer    *time.Timer
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

// seen notes the line git printed last, kept or not, as the progress.
func (b *batcher) seen(l string, percent int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.progress, b.percent, b.moved = l, percent, true
	if b.timer == nil {
		b.timer = time.AfterFunc(flushEvery, b.flush)
	}
}

func (b *batcher) flush() {
	b.mu.Lock()
	lines := b.pending
	progress, percent, moved := b.progress, b.percent, b.moved
	b.pending, b.timer, b.moved = nil, nil, false
	b.mu.Unlock()
	if len(lines) > 0 || moved {
		b.update(func() {
			b.e.add(lines)
			if moved {
				b.e.Progress, b.e.Percent = redact.Shown(progress), percent
			}
		})
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
		b.e.Running, b.e.Ended = false, ended
		if err != nil {
			b.e.Err = shownError{err}
		}
	})
}

// follow logs a write job's output and result, and reads everything
// again once it ends.
func (s *Session) follow(e *LogEntry, j *write.Job) { s.followThen(e, j, repo.Rescan) }

// followThen is follow reading again only what, for a write that changes
// less than everything.
func (s *Session) followThen(e *LogEntry, j *write.Job, what repo.Change) {
	// Every write the log follows is one the user waits on, but for the
	// quiet fetch itself and the quiet writes: the queue runs one at a
	// time, so a fetch in the background, which may take minutes, gives
	// its turn up and is tried again later. A commit-graph the session
	// writes on its own gives it up to any write of the user's too, but
	// not to the background fetch: one that takes longer to write than
	// the time between fetches would never be written.
	if bg := s.fetch.job; bg != nil && bg != j && !e.Quiet {
		bg.Cancel()
	}
	if g := s.graph.job; g != nil && g != j && !e.Quiet {
		g.Cancel()
	}
	e.job, e.Percent = j, -1
	b := &batcher{update: s.update, e: e}
	go func() {
		defer s.recoverWork()
		for ev := range j.Events() {
			if ev.Percent < 0 || strings.Contains(ev.Line, "done") {
				b.line(ev.Line) // progress redraws are not kept, only their last state
			}
			if strings.TrimSpace(ev.Line) != "" {
				b.seen(ev.Line, ev.Percent)
			}
		}
		_, err := j.Result()
		b.finish(err, s.timeNow())
		fetched := err == nil && !e.Quiet && len(j.Op.Cmd.Args) > 0 && j.Op.Cmd.Args[0] == "fetch"
		s.update(func() {
			s.Writes++
			if fetched {
				// The remote answered the user: the background fetch
				// that stopped for want of them may go on.
				s.ResumeFetch()
			}
		})
		s.refreshNow(what)
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
		s.refreshNow(repo.Rescan)
	})
	return e
}

// shownError is an error as the log shows it: stripped of control
// characters and secrets, its cause still reachable.
type shownError struct{ err error }

func (e shownError) Error() string { return redact.Shown(e.err.Error()) }
func (e shownError) Unwrap() error { return e.err }

// CommandsVersion changes whenever git runs a command anywhere in the app,
// so a view of GitCommands can tell it is behind without a lock.
func (s *Session) CommandsVersion() uint64 { return s.runner.Journal().Version() }

// GitCommands reads, off the main thread, the git commands of this
// repository the runner still keeps, reads included, oldest first, and
// hands them to done on it: none while the repository is not open, or
// once it closed and opened again (Trust), for the view to ask again.
func (s *Session) GitCommands(done func([]run.Record)) {
	r := s.Repo
	if r == nil {
		done(nil)
		return
	}
	j := s.runner.Journal()
	r.Life().Go(func(ctx context.Context) {
		recs := j.For(r.GitDir)
		s.handOver(r, func() { done(recs) }, func() { done(nil) })
	})
}
