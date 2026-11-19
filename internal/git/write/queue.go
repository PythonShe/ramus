// Package write runs the commands that change a repository, one at a
// time per repository, through git, with their output live and a way to
// cancel them.
package write

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	// Line, when set, is the command the log shows in place of Cmd's.
	Line string
	Cmd  run.Cmd
	// Input is fed to the command's standard input each time it runs, a
	// redo included; use it instead of Cmd.Stdin, which is read once.
	Input []byte
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
	// Pre runs before Cmd, after the checks, snapshot and backup, with
	// its output in the job's: the write stops if it fails. A switch that
	// stashes the changes first uses it.
	Pre *run.Cmd
	// PreStashes says Pre stashes the local changes: when Cmd then fails
	// with HEAD where it was, the stash it made is popped back, so the
	// changes are not left in a stash the user never hears of.
	PreStashes bool
	// More run after Cmd, in order, for a write git takes in parts: paths
	// past what one command line holds. The first that fails stops it.
	More []run.Cmd
	// File, when set, is written into the work tree after the checks and
	// snapshot and before Pre: a merged result the user resolved.
	File *FileWrite
	// Hold, when set, is held while the write runs: a write that must not
	// run beside a read the caller holds it for, as git-lfs, whose lock
	// cache two of its commands at once can read half written.
	Hold sync.Locker
	// Refs are the refs the write may move besides the branch HEAD names
	// before and after it (a branch it creates, deletes or renames, a
	// tag). Undo moves only these, so a ref moved meanwhile by something
	// else is left alone.
	Refs []string
	// NoIndex marks a write that never takes index.lock, as a fetch or a
	// push: it does not wait for another command's lock to go.
	NoIndex bool
	// RefsOnly marks a write that moves refs and nothing else, as a fetch
	// does: the work tree, the index and HEAD stay as they were, so what
	// follows it need not read status.
	RefsOnly bool
	// do runs in place of Cmd, for writes the engine makes of several
	// commands: undo and redo, snapshots.
	do func(ctx context.Context, j *Job) (run.Result, error)
	// leaves names a file in the git dir the write may make for a later
	// command, as a squash merge's SQUASH_MSG: when the write made it,
	// undo removes it, for nothing it describes is left.
	leaves string
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
	// config is the settings of the branches the write may move, read
	// before it ran, for undo to put back.
	config branchConfig
	// dropped counts events the reader was too slow for; Result still
	// holds the output.
	dropped int
	// entry is the History entry the write recorded, 0 for none.
	entry uint64
	// stash is the stash the write's Pre made of the local changes, ""
	// for none.
	stash string
	// left is whether Op.leaves was there before the write ran.
	left bool
}

// KeptError is a write that failed after keeping a snapshot of what it
// was about to change: whatever it changed before it stopped is in
// Snapshot. With Undo set the write recorded an entry, and Undo puts
// that back; else the snapshot list holds it.
type KeptError struct {
	Err      error
	Snapshot snapshot.Snapshot
	Undo     bool
}

func (e *KeptError) Error() string {
	how := "the snapshot list has it"
	if e.Undo {
		how = "Undo puts it back"
	}
	return fmt.Sprintf("%v; what it changed is kept in the snapshot taken %s, and %s", e.Err, e.Snapshot.Time.Format(time.DateTime), how)
}

func (e *KeptError) Unwrap() error { return e.Err }

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

// Entry is the id of the History entry the job recorded, once Done is
// closed; 0 when it recorded none, as a write not undoable, failed, or
// whose result could not be read. History.Last tells whether Undo would
// still take it back.
func (j *Job) Entry() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.entry
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
	life   *lifetime.Lifetime
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
	q := &Queue{r: r, life: life, gitDir: gitDir, LockWait: DefaultLockWait, wake: make(chan struct{}, 1)}
	q.opened(true)
	if err := life.Go(q.loop); err != nil {
		q.opened(false)
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
	defer q.opened(false)
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
		res, err := q.runSafe(ctx, j)
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

// runSafe runs j, ending it with an error if it panics: the loop goes on
// to the jobs after it, and whoever waits on it hears it ended.
func (q *Queue) runSafe(ctx context.Context, j *Job) (res run.Result, err error) {
	defer func() {
		if v := recover(); v != nil {
			res, err = run.Result{}, fmt.Errorf("write: %s stopped on an internal error: %v", j.Op.Name, v)
			q.life.Report(v)
		}
	}()
	return q.run(ctx, j)
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
	if !j.Op.NoIndex {
		if err := waitLock(ctx, q.gitDir, q.LockWait); err != nil {
			return run.Result{}, err
		}
	}
	if h := j.Op.Hold; h != nil {
		h.Lock()
		defer h.Unlock()
	}
	if j.Op.do != nil {
		res, err := j.Op.do(ctx, j)
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
	if f := j.Op.File; f != nil {
		if err := f.write(j.Op.Cmd.WorkTree); err != nil {
			return run.Result{}, q.kept(j, err, false)
		}
	}
	var stashed *preStash
	// unread is why the stash Pre may have made could not be read: a
	// write failing after it says the changes may be in the stash list.
	var unread error
	if j.Op.Pre != nil {
		if j.Op.PreStashes {
			stashed = q.beforeStash(ctx, j.Op.Cmd.WorkTree)
		}
		res, preErr := execute(ctx, q.r, j, *j.Op.Pre, nil)
		if stashed != nil {
			// Read though the job was cancelled meanwhile: a stash made
			// and not known of would keep the changes from the user.
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unstashTimeout)
			made, err := q.ownStash(sctx, j.Op.Cmd.WorkTree, stashed.stash)
			cancel()
			if err != nil {
				stashed, unread = nil, err
			} else {
				stashed.made = made
				if made != "" && made != stashed.stash {
					j.mu.Lock()
					j.stash = made
					j.mu.Unlock()
				}
			}
		}
		if preErr != nil {
			if stashed != nil {
				preErr = q.unstash(ctx, j, stashed, preErr)
			}
			return res, q.kept(j, preErr, false)
		}
	}
	if j.Op.leaves != "" {
		_, err := os.Lstat(filepath.Join(j.Op.Cmd.GitDir, j.Op.leaves))
		j.mu.Lock()
		j.left = err == nil
		j.mu.Unlock()
	}
	res, err := execute(ctx, q.r, j, j.Op.Cmd, j.Op.Input)
	for _, c := range j.Op.More {
		if err != nil {
			break
		}
		res, err = execute(ctx, q.r, j, c, nil)
	}
	if err != nil {
		if le := lockError(q.gitDir, res.Stderr); le != nil {
			err = le
		}
		switch {
		case stashed != nil:
			err = q.unstash(ctx, j, stashed, err)
		case unread != nil:
			err = &StashKeptError{Err: err, Pop: unread}
		}
	}
	j.mu.Lock()
	snapped := j.snap.Commit != ""
	j.mu.Unlock()
	// A write of files that failed part way may have dropped work all the
	// same, a discard with a filter that failed after it unstaged, or a
	// delete cancelled between its runs: undo puts back what it kept.
	if record && (err == nil || snapped && j.Op.Undo == FilesOnly) {
		q.record(j, before, err != nil)
	}
	return res, q.kept(j, err, true)
}

// failed is err, from a command a job that sets its own do ran past its
// checks and snapshot: a lock found held says so, and so does the
// snapshot where what the job changed is kept, as for any other write.
func (q *Queue) failed(j *Job, res run.Result, err error) error {
	if err == nil {
		return nil
	}
	if le := lockError(q.gitDir, res.Stderr); le != nil {
		err = le
	}
	return q.kept(j, err, false)
}

// kept is err, from a write that ran past its checks, saying where what
// the write changed is kept, when it kept a snapshot first; recorded is
// whether it may have recorded an entry for Undo.
func (q *Queue) kept(j *Job, err error, recorded bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err == nil || j.snap.Commit == "" {
		return err
	}
	return &KeptError{Err: err, Snapshot: j.snap, Undo: recorded && j.entry != 0}
}

// record pushes the entry undo of j takes; failed is a write that failed,
// recorded only when it changed what its snapshot holds.
func (q *Queue) record(j *Job, before fullState, failed bool) {
	after, err := capture(q.Refs)
	if err != nil {
		return // the write happened; it just cannot be undone
	}
	b, a := touched(before, after, j.Op.Refs)
	j.mu.Lock()
	snap, config, backup, stash, left := j.snap, j.config, j.backup, j.stash, j.left
	j.mu.Unlock()
	if j.Op.Undo == MovesHead && snap.Commit == "" && stash == "" && a.Symbolic == b.Symbolic && a.Head == b.Head && len(a.Refs) == 0 {
		// Nothing moved, as a merge or pull already up to date: there is
		// nothing to undo, and an undo would only unstage.
		return
	}
	e := Entry{Name: j.Op.Name, Kind: j.Op.Undo, Time: time.Now(), Before: b, After: a, Snapshot: snap, op: j.Op, backup: backup, stash: stash}
	if j.Op.leaves != "" && !left {
		if _, err := os.Lstat(filepath.Join(j.Op.Cmd.GitDir, j.Op.leaves)); err == nil {
			e.made = j.Op.leaves
		}
	}
	q.settle(&e, config)
	if failed && !e.unknown && len(e.paths) == 0 {
		// Refused before it changed anything, as a squash merge over local
		// changes it would overwrite: an undo would do nothing.
		return
	}
	id := q.History.push(e)
	j.mu.Lock()
	j.entry = id
	j.mu.Unlock()
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
		// A file saved while git read the files, through filters that take
		// their time, may be in neither the snapshot nor what the write
		// leaves: the files are looked at once more.
		if x := op.Expect; x != nil && len(x.Files) > 0 {
			if err := checkFiles(op.Cmd.WorkTree, x.Files); err != nil {
				return err
			}
		}
	}
	if names := branchNames(op.Refs); len(names) > 0 && op.Undo != NotUndoable && q.Refs != nil {
		config, err := q.branchConfig(ctx, op.Cmd.WorkTree, names)
		if err != nil {
			return fmt.Errorf("write: reading the branches' settings first: %w", err)
		}
		j.mu.Lock()
		j.config = config
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

// execute runs one of the job's commands with its output going to its
// events.
func execute(ctx context.Context, r *run.Runner, j *Job, cmd run.Cmd, input []byte) (run.Result, error) {
	cmd.Read = false
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
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
