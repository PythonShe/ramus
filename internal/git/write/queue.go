// Package write runs the commands that change a repository, one at a
// time per repository, through git, with their output live and a way to
// cancel them.
package write

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"ramus/internal/git/lifetime"
	"ramus/internal/git/refs"
	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

// Op is one write: a git command and what to call it.
type Op struct {
	// Name is what the app calls it ("Commit", "Fetch origin").
	Name string
	Cmd  run.Cmd
	// Expect, when set, makes the write refuse with a *StaleError if the
	// state it was decided on changed.
	Expect *Expect
	// Snapshot, when set, keeps the files it names before the write runs;
	// the write does not run if that fails.
	Snapshot *snapshot.Options
	// Backup names refs to keep under BackupPrefix before a history
	// rewrite.
	Backup []string
	// Undo says how to undo the write; a write that succeeds is then
	// recorded in the queue's History.
	Undo UndoKind
	// Refs are the refs the write may move besides the branch HEAD names
	// before and after it (a branch it creates, deletes or renames, a
	// tag). Undo moves only these, so a ref moved meanwhile by something
	// else is left alone.
	Refs []string
	// do runs in place of Cmd, for undo and redo.
	do func(ctx context.Context) (run.Result, error)
}

// Stream says where a line came from.
type Stream uint8

const (
	Stdout Stream = iota
	Stderr
)

// Event is a line of output, or a progress update, as it comes.
type Event struct {
	Stream Stream
	// Line is the text without its line ending; a progress line repeats as
	// git redraws it.
	Line string
	// Percent is the progress a line reports, or -1.
	Percent int
}

// Job is a submitted write.
type Job struct {
	Op     Op
	q      *Queue
	ctx    context.Context
	events chan Event
	done   chan struct{}
	cancel context.CancelFunc
	mu     sync.Mutex
	res    run.Result
	err    error
	snap   snapshot.Snapshot
	backup string
	// dropped counts events the reader was too slow for; Result still
	// holds the output.
	dropped int
}

// Events delivers output as it comes; it is closed when the job ends.
func (j *Job) Events() <-chan Event { return j.events }

// Done is closed when the job has ended.
func (j *Job) Done() <-chan struct{} { return j.done }

// Cancel stops the job: a queued job ends at once and never runs; a
// running one has git stopped.
func (j *Job) Cancel() {
	j.cancel()
	q := j.q
	if q == nil {
		return // started on its own: stopping its context stops git
	}
	q.mu.Lock()
	queued := false
	for i, k := range q.jobs {
		if k == j && !(i == 0 && q.started) {
			copy(q.jobs[i:], q.jobs[i+1:])
			q.jobs[len(q.jobs)-1] = nil // let the job go
			q.jobs = q.jobs[:len(q.jobs)-1]
			queued = true
			break
		}
	}
	q.mu.Unlock()
	if queued {
		j.finish(run.Result{}, context.Canceled)
	}
}

// Result returns what the job produced, once Done is closed. The error
// is a *run.Error with git's stderr as git wrote it, a *LockedError, or
// a context error after Cancel.
func (j *Job) Result() (run.Result, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.res, j.err
}

// Kept returns the snapshot taken and the backup directory made before the
// job ran, if it asked for them.
func (j *Job) Kept() (snapshot.Snapshot, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snap, j.backup
}

// Queue runs one repository's writes in order, one at a time.
type Queue struct {
	r      *run.Runner
	gitDir string
	// LockWait is how long a write waits for another process's index.lock
	// to go before failing with a LockedError.
	LockWait time.Duration
	// Refs reads the repository's refs, for undo; without it nothing is
	// recorded.
	Refs refs.Reader
	// History is what Undo and Redo act on.
	History History
	mu      sync.Mutex
	jobs    []*Job
	// started is set while jobs[0] runs.
	started bool
	closed  error
	wake    chan struct{}
}

// DefaultLockWait covers another git command finishing.
const DefaultLockWait = 5 * time.Second

// NewQueue starts the queue of a repository, which ends with its
// lifetime; jobs still queued then are cancelled.
func NewQueue(life *lifetime.Lifetime, r *run.Runner, gitDir string) (*Queue, error) {
	q := &Queue{r: r, gitDir: gitDir, LockWait: DefaultLockWait, wake: make(chan struct{}, 1)}
	if err := life.Go(q.loop); err != nil {
		return nil, err
	}
	return q, nil
}

// Submit queues a write. It never blocks. After the repository closed,
// the job ends at once with lifetime.ErrClosed.
func (q *Queue) Submit(op Op) *Job {
	j := newJob(q, op)
	q.mu.Lock()
	closed := q.closed
	if closed == nil {
		q.jobs = append(q.jobs, j)
	}
	q.mu.Unlock()
	if closed != nil {
		j.cancel()
		j.finish(run.Result{}, closed)
		return j
	}
	q.poke()
	return j
}

// Pending returns the number of jobs waiting or running.
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

func (q *Queue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Queue) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			q.closeAll(lifetime.ErrClosed)
			return
		}
		q.mu.Lock()
		var j *Job
		if len(q.jobs) > 0 {
			j = q.jobs[0]
			q.started = true
		}
		q.mu.Unlock()
		if j == nil {
			select {
			case <-q.wake:
			case <-ctx.Done():
			}
			continue
		}
		res, err := q.run(ctx, j)
		// Out of the queue before Done, so Pending never counts a job
		// that has ended.
		q.mu.Lock()
		q.jobs[0] = nil
		q.jobs = q.jobs[1:]
		q.started = false
		q.mu.Unlock()
		j.finish(res, err)
	}
}

func (q *Queue) closeAll(err error) {
	q.mu.Lock()
	jobs := q.jobs
	q.jobs = nil
	q.closed = err
	q.mu.Unlock()
	for _, j := range jobs {
		j.finish(run.Result{}, err)
	}
}

func (j *Job) finish(res run.Result, err error) {
	j.mu.Lock()
	j.res, j.err = res, err
	j.mu.Unlock()
	close(j.events)
	close(j.done)
}

func (q *Queue) run(qctx context.Context, j *Job) (run.Result, error) {
	ctx, stop := context.WithCancel(qctx)
	defer stop()
	go func() {
		select {
		case <-j.ctx.Done():
			stop()
		case <-ctx.Done():
		}
	}()
	if err := j.ctx.Err(); err != nil {
		return run.Result{}, err
	}
	if err := waitLock(ctx, q.gitDir, q.LockWait); err != nil {
		return run.Result{}, err
	}
	if j.Op.do != nil {
		res, err := j.Op.do(ctx)
		return res, err
	}
	var before fullState
	record := j.Op.Undo != NotUndoable && q.Refs != nil
	if record {
		var err error
		if before, err = capture(q.Refs); err != nil {
			return run.Result{}, err
		}
	}
	if err := q.prepare(ctx, j); err != nil {
		return run.Result{}, err
	}
	res, err := execute(ctx, q.r, j)
	if err != nil {
		if le := lockError(q.gitDir, res.Stderr); le != nil {
			err = le
		}
	}
	if err == nil && record {
		q.record(j, before)
	}
	return res, err
}

func (q *Queue) record(j *Job, before fullState) {
	after, err := capture(q.Refs)
	if err != nil {
		return // the write happened; it just cannot be undone
	}
	b, a := touched(before, after, j.Op.Refs)
	j.mu.Lock()
	snap := j.snap
	j.mu.Unlock()
	q.History.push(Entry{Name: j.Op.Name, Kind: j.Op.Undo, Time: time.Now(), Before: b, After: a, Snapshot: snap, op: j.Op})
}

// prepare runs inside the job's turn, so nothing the queue runs can
// change the state between the check and the write.
func (q *Queue) prepare(ctx context.Context, j *Job) error {
	op := j.Op
	if op.Expect != nil {
		if err := check(ctx, q.r, q.gitDir, op.Cmd.WorkTree, op.Expect); err != nil {
			return err
		}
	}
	if op.Snapshot != nil {
		s, err := snapshot.Take(ctx, q.r, snapshot.Where{GitDir: q.gitDir, WorkTree: op.Cmd.WorkTree}, *op.Snapshot)
		if err != nil {
			return fmt.Errorf("write: keeping a snapshot first: %w", err)
		}
		j.mu.Lock()
		j.snap = s
		j.mu.Unlock()
	}
	if len(op.Backup) > 0 {
		dir, err := backup(ctx, q.r, q.gitDir, op.Backup)
		if err != nil {
			return fmt.Errorf("write: keeping a backup first: %w", err)
		}
		j.mu.Lock()
		j.backup = dir
		j.mu.Unlock()
	}
	return nil
}

// execute runs the job's command with its output going to its events.
func execute(ctx context.Context, r *run.Runner, j *Job) (run.Result, error) {
	cmd := j.Op.Cmd
	cmd.Read = false
	out, errw := &lines{j: j, stream: Stdout}, &lines{j: j, stream: Stderr}
	stdout := &keep{max: maxStdout}
	cmd.Stdout = teeWriter{stdout, out}
	cmd.Stderr = errw
	res, err := r.Run(ctx, &cmd)
	out.flush()
	errw.flush()
	res.Stdout = stdout.buf
	return res, err
}

func newJob(q *Queue, op Op) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	return &Job{Op: op, q: q, ctx: ctx, events: make(chan Event, 1024), done: make(chan struct{}), cancel: cancel}
}

// maxStdout bounds the output a write keeps; its events carry it all.
const maxStdout = 16 << 20

type keep struct {
	buf []byte
	max int
}

func (k *keep) Write(p []byte) (int, error) {
	if room := k.max - len(k.buf); room > 0 {
		k.buf = append(k.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

type teeWriter struct {
	a, b interface{ Write([]byte) (int, error) }
}

func (t teeWriter) Write(p []byte) (int, error) {
	t.a.Write(p)
	return t.b.Write(p)
}

var progressRE = regexp.MustCompile(`:\s+(\d{1,3})% \(`)

// lines splits output at \n and \r (git redraws progress with \r) into
// events, without blocking git on a slow reader.
type lines struct {
	j      *Job
	stream Stream
	buf    []byte
}

func (l *lines) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexAny(p, "\r\n")
		if i < 0 {
			if len(l.buf) < 64<<10 {
				l.buf = append(l.buf, p...)
			}
			break
		}
		l.buf = append(l.buf, p[:i]...)
		l.emit()
		p = p[i+1:]
	}
	return n, nil
}

func (l *lines) flush() {
	if len(l.buf) > 0 {
		l.emit()
	}
}

func (l *lines) emit() {
	line := string(l.buf)
	l.buf = l.buf[:0]
	if line == "" {
		return
	}
	ev := Event{Stream: l.stream, Line: line, Percent: -1}
	if m := progressRE.FindStringSubmatch(line); m != nil {
		ev.Percent, _ = strconv.Atoi(m[1])
	}
	select {
	case l.j.events <- ev:
	default:
		l.j.mu.Lock()
		l.j.dropped++
		l.j.mu.Unlock()
	}
}
