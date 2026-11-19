package write

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ramus/internal/git/refs"
	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

// UndoKind says how a write is undone; the default is not at all.
type UndoKind uint8

const (
	NotUndoable UndoKind = iota
	// RefsOnly writes move refs and leave the files: commit, amend,
	// branch create, delete and rename, tag. Undo puts the refs back.
	RefsOnly
	// MovesHead writes take the work tree with HEAD: checkout, merge,
	// reset, rebase, cherry-pick, revert. Undo checks out or resets
	// (--keep) back, then restores the work the write dropped from its
	// snapshot.
	MovesHead
	// FilesOnly writes change files only: discard, clean. Undo restores
	// them from the write's snapshot; redo runs the write again.
	FilesOnly
)

// State is HEAD and the refs a write touched, before or after it.
type State struct {
	// Symbolic is the branch HEAD names, "" when detached.
	Symbolic string
	Head     string // hex, "" when unborn
	// Refs maps each touched ref to its value, "" when absent.
	Refs map[string]string
}

// Entry is a write that can be undone.
type Entry struct {
	Name          string
	Kind          UndoKind
	Time          time.Time
	Before, After State
	// Snapshot is the work the write kept before it ran, if any.
	Snapshot snapshot.Snapshot
	op       Op
	// config is the settings of the branches the write may move, before
	// and after it.
	config [2]branchConfig
	// left is the paths Snapshot covers as the write, or its last redo,
	// left them, nil when not known: what an undo must find, so as not to
	// overwrite work done since.
	left *pathState
	// id tells this entry from every other of its History, for an undo
	// that must take back this write and no other.
	id uint64
	// backup is the directory of the refs the write backed up, "" for
	// none: kept from expiring while the entry is in the History.
	backup string
	// paths are what an undo puts back from Snapshot (touchedPaths);
	// unknown when they could not be read, and then none is.
	paths   []string
	unknown bool
}

// History holds a repository's undoable writes, for as long as the
// repository is open.
type History struct {
	mu     sync.Mutex
	done   []Entry
	undone []Entry
	// seq is the id the last entry pushed got.
	seq uint64
}

// MaxHistory bounds the entries kept; the oldest go first.
const MaxHistory = 100

// MaxHistoryBytes bounds the memory the entries hold, as a patch a
// discard of lines keeps to redo it, or the state of the many files a
// delete of a large folder left; the oldest go first, the last write
// always stays undoable.
const MaxHistoryBytes = 64 << 20

// size is about the memory e holds.
func (e *Entry) size() int {
	n := 512 + len(e.op.Input) + len(e.Name) + pathsSize(e.Snapshot.Paths) + pathsSize(e.op.Cmd.Args) + pathsSize(e.paths)
	for _, c := range e.op.More {
		n += pathsSize(c.Args)
	}
	if o := e.op.Snapshot; o != nil {
		n += pathsSize(o.Paths)
	}
	if l := e.left; l != nil {
		n += len(l.index) + len(l.files)*96 + len(l.sums)*48
		for _, f := range l.files {
			n += len(f.Path)
		}
	}
	return n
}

func pathsSize(paths []string) int {
	n := len(paths) * 16
	for _, p := range paths {
		n += len(p)
	}
	return n
}

// trim drops the oldest entries past MaxHistoryBytes: those to undo
// first, then those furthest to redo, keeping the last write. h.mu is
// held.
func (h *History) trim() {
	total := 0
	for _, side := range [][]Entry{h.done, h.undone} {
		for i := range side {
			total += side[i].size()
		}
	}
	for total > MaxHistoryBytes && len(h.done) > 1 {
		total -= h.done[0].size()
		h.done = slices.Delete(h.done, 0, 1)
	}
	for total > MaxHistoryBytes && len(h.undone) > 0 && len(h.done)+len(h.undone) > 1 {
		total -= h.undone[0].size()
		h.undone = slices.Delete(h.undone, 0, 1)
	}
}

// Next names the write Undo and Redo would act on, "" when none, for the
// menu ("Undo Commit").
func (h *History) Next() (undo, redo string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.done); n > 0 {
		undo = h.done[n-1].Name
	}
	if n := len(h.undone); n > 0 {
		redo = h.undone[n-1].Name
	}
	return undo, redo
}

// Last is the id of the write Undo would act on, 0 when none: as a Job's
// Entry, to tell whether Undo would still take that write back.
func (h *History) Last() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.done); n > 0 {
		return h.done[n-1].id
	}
	return 0
}

// push records e and returns the id it gave it.
func (h *History) push(e Entry) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.id = h.seq
	h.done = append(h.done, e)
	if len(h.done) > MaxHistory {
		h.done = slices.Delete(h.done, 0, len(h.done)-MaxHistory)
	}
	h.undone = nil
	h.trim()
	return e.id
}

// backups are the backup directories of the entries held.
func (h *History) backups() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]bool{}
	for _, side := range [][]Entry{h.done, h.undone} {
		for _, e := range side {
			if e.backup != "" {
				out[e.backup] = true
			}
		}
	}
	return out
}

// ErrNothingToUndo is an Undo or Redo with nothing on its side.
var ErrNothingToUndo = errors.New("write: nothing to undo or redo")

// ErrNotLast is an UndoEntry of a write that is no longer the last
// undoable one: another came after it, or it was undone already.
var ErrNotLast = errors.New("write: another write came since")

// ignored are refs undo neither records nor moves: its own snapshots and
// backups.
func ignored(name string) bool {
	return strings.HasPrefix(name, snapshot.Prefix) || strings.HasPrefix(name, BackupPrefix)
}

type fullState struct {
	symbolic, head string
	refs           map[string]string
}

func capture(rd refs.Reader) (fullState, error) {
	h, err := rd.Head()
	if err != nil {
		return fullState{}, err
	}
	l, err := rd.List()
	if err != nil {
		return fullState{}, err
	}
	s := fullState{symbolic: h.Target, head: hex.EncodeToString(h.OID), refs: make(map[string]string, l.Len())}
	for i := range l.Len() {
		if name := l.Name(i); l.Symbolic(i) == "" && !ignored(name) {
			s.refs[name] = hex.EncodeToString(l.OID(i))
		}
	}
	return s, nil
}

// touched turns two full states into the entry's two states, keeping the
// refs that changed among those the write may move: the branch HEAD named
// before and after, and extra.
func touched(a, b fullState, extra []string) (State, State) {
	before := State{Symbolic: a.symbolic, Head: a.head, Refs: map[string]string{}}
	after := State{Symbolic: b.symbolic, Head: b.head, Refs: map[string]string{}}
	names := append([]string{a.symbolic, b.symbolic}, extra...)
	for _, name := range names {
		if name == "" || ignored(name) {
			continue
		}
		if a.refs[name] != b.refs[name] {
			before.Refs[name], after.Refs[name] = a.refs[name], b.refs[name]
		}
	}
	return before, after
}

// Undo queues undoing the last undoable write; it acts on whatever is
// last when its turn comes.
func (q *Queue) Undo() *Job { return q.submitHistory("Undo", true, 0) }

// UndoEntry queues undoing the write a Job's Entry names; when its turn
// comes, it refuses with ErrNotLast unless that write is the last
// undoable one. An id of 0, a write that recorded no entry, names none:
// it refuses too, rather than undo whatever write is last.
func (q *Queue) UndoEntry(id uint64) *Job {
	if id == 0 {
		return q.Submit(Op{Name: "Undo", do: func(context.Context, *Job) (run.Result, error) {
			return run.Result{}, ErrNotLast
		}})
	}
	return q.submitHistory("Undo", true, id)
}

// Redo queues redoing the last undone write.
func (q *Queue) Redo() *Job { return q.submitHistory("Redo", false, 0) }

func (q *Queue) submitHistory(name string, undo bool, id uint64) *Job {
	return q.Submit(Op{Name: name, do: func(ctx context.Context, _ *Job) (run.Result, error) {
		return q.travel(ctx, undo, id)
	}})
}

// travel undoes or redoes the last write on its side; id, when not 0,
// is the entry that must be.
func (q *Queue) travel(ctx context.Context, undo bool, id uint64) (run.Result, error) {
	h := &q.History
	h.mu.Lock()
	from := &h.done
	if !undo {
		from = &h.undone
	}
	if len(*from) == 0 {
		h.mu.Unlock()
		return run.Result{}, ErrNothingToUndo
	}
	e := (*from)[len(*from)-1]
	h.mu.Unlock()
	if id != 0 && e.id != id {
		return run.Result{}, ErrNotLast
	}
	if q.Refs == nil {
		return run.Result{}, errors.New("write: undo needs the repository's refs")
	}
	var err error
	switch {
	case undo:
		if err = q.unchanged(ctx, e); err == nil {
			err = q.apply(ctx, e, e.After, e.Before, true)
		}
	case e.Kind == FilesOnly || e.Kind == MovesHead && e.op.Snapshot != nil:
		// A write that dropped work is redone by running it again, after
		// checking nothing moved, since only it knows what it drops. Its
		// fresh snapshot is what a later undo restores.
		if err = q.verify(e.Before); err == nil {
			var fresh snapshot.Snapshot
			var ran bool
			fresh, ran, err = q.redoFiles(ctx, e)
			if ran && fresh.Commit != "" {
				// Run, if only in part: what it dropped is in the fresh
				// snapshot, which the next undo puts back.
				e.Snapshot = fresh
				if err != nil {
					err = &KeptError{Err: err, Snapshot: fresh, Undo: true}
				}
			}
		}
	default:
		err = q.apply(ctx, e, e.Before, e.After, false)
	}
	if err != nil && !moved(err) {
		return run.Result{}, err
	}
	if !undo {
		q.settlePaths(ctx, &e)
	}
	h.mu.Lock()
	*from = (*from)[:len(*from)-1]
	if undo {
		h.undone = append(h.undone, e)
	} else {
		h.done = append(h.done, e)
	}
	h.trim()
	h.mu.Unlock()
	return run.Result{}, err
}

// moved reports an undo or redo that failed once the repository had
// moved, which its entry moves with, the error saying what is left: the
// branches' settings, the rest after HEAD, or a redo that ran part way.
func moved(err error) bool {
	var ce *configError
	var pe *partlyError
	var ke *KeptError
	return errors.As(err, &ce) || errors.As(err, &pe) || errors.As(err, &ke) && ke.Undo
}

// apply moves the repository from state from to state to, refusing if it
// is not in from: something else changed it since.
func (q *Queue) apply(ctx context.Context, e Entry, from, to State, restore bool) error {
	if err := q.verify(from); err != nil {
		return err
	}
	var err error
	w := snapshot.Where{GitDir: q.gitDir, WorkTree: e.op.Cmd.WorkTree}
	git := func(args ...string) error {
		_, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: w.WorkTree, Args: args, Timeout: HookTimeout})
		return err
	}
	switch e.Kind {
	case FilesOnly:
		return restoreFiles(ctx, q, w, e)
	case MovesHead:
		if _, err := snapshot.Take(ctx, q.r, w, snapshot.Options{Reason: "Before " + verb(restore) + " " + e.Name}); err != nil {
			return fmt.Errorf("write: keeping a snapshot first: %w", err)
		}
	}
	// Refs that will exist are set before HEAD moves, so a checkout finds
	// its branch; deletions and the branch HEAD leaves come after. HEAD's
	// own branch, when it stays on it, moves first too, its index and
	// files after it.
	same := e.Kind == MovesHead && from.Symbolic != "" && from.Symbolic == to.Symbolic
	early := func(name, nu string) bool {
		return e.Kind == MovesHead && nu != "" && (name != from.Symbolic || same)
	}
	if err := q.moveRefs(ctx, from, to, early, false); err != nil {
		return err
	}
	if e.Kind == MovesHead {
		switch {
		case from.Symbolic != to.Symbolic && to.Symbolic != "":
			err = git("checkout", "-q", "--no-guess", strings.TrimPrefix(to.Symbolic, "refs/heads/"), "--")
		case !same:
			err = git("checkout", "-q", "--detach", to.Head, "--")
		default:
			// As a checkout does, not as git reset --keep, which also
			// unstages every change: what was staged since stays, and a
			// file changed since that the move would overwrite stops it.
			// A soft reset left the index and files; a mixed one the files.
			switch resetMode(e.op) {
			case "soft":
			case "mixed":
				err = git("read-tree", "-m", "-i", from.Head, to.Head)
			default:
				err = git("read-tree", "-m", "-u", from.Head, to.Head)
			}
		}
		if err != nil {
			// Put back what moved early, so the history still matches,
			// though the undo was cancelled: that may be why it failed.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
			rb := q.moveRefs(rctx, to, from, func(name, nu string) bool { return early(name, to.Refs[name]) }, false)
			cancel()
			if rb != nil {
				return fmt.Errorf("%w (and putting refs back failed: %v)", err, rb)
			}
			return err
		}
	}
	late := func(name, nu string) bool { return !early(name, nu) }
	if err := q.moveRefs(ctx, from, to, late, e.Kind == RefsOnly); err != nil {
		if e.Kind == MovesHead {
			return &partlyError{err: err, undo: restore}
		}
		return err
	}
	// From here HEAD and the refs are moved: a failure leaves the rest
	// undone, and the entry goes with HEAD.
	if e.Kind == RefsOnly && from.Symbolic != to.Symbolic && to.Symbolic != "" {
		if err := git("symbolic-ref", "HEAD", to.Symbolic); err != nil {
			return &partlyError{err: err, undo: restore}
		}
	}
	if restore && e.Kind == MovesHead && e.Snapshot.Commit != "" {
		if err := restoreFiles(ctx, q, w, e); err != nil {
			return &partlyError{err: err, undo: restore}
		}
	}
	cf, ct := e.config[1], e.config[0]
	if !restore {
		cf, ct = ct, cf
	}
	return q.setBranchConfig(ctx, w.WorkTree, cf, ct)
}

// rollbackTimeout bounds putting refs back after an undo failed part way.
const rollbackTimeout = time.Minute

// verify refuses unless HEAD and the touched refs are as in s.
func (q *Queue) verify(s State) error {
	now, err := capture(q.Refs)
	if err != nil {
		return err
	}
	if now.symbolic != s.Symbolic || now.head != s.Head {
		return &StaleError{What: "HEAD", Saw: short(s.Head), Now: short(now.head)}
	}
	for name, v := range s.Refs {
		if now.refs[name] != v {
			return &StaleError{What: name, Saw: short(v), Now: short(now.refs[name])}
		}
	}
	return nil
}

func verb(undo bool) string {
	if undo {
		return "undoing"
	}
	return "redoing"
}

// moveRefs sets the touched refs that pick allows from their from value
// to their to value in one transaction; with head, a detached HEAD too.
func (q *Queue) moveRefs(ctx context.Context, from, to State, pick func(name, nu string) bool, head bool) error {
	var in strings.Builder
	for _, name := range slices.Sorted(maps.Keys(to.Refs)) {
		old, nu := from.Refs[name], to.Refs[name]
		if !pick(name, nu) {
			continue
		}
		switch {
		case nu == "":
			fmt.Fprintf(&in, "delete %s %s\n", name, old)
		case old == "":
			fmt.Fprintf(&in, "create %s %s\n", name, nu)
		default:
			fmt.Fprintf(&in, "update %s %s %s\n", name, nu, old)
		}
	}
	if head && from.Symbolic == "" && to.Symbolic == "" && from.Head != to.Head && to.Head != "" {
		fmt.Fprintf(&in, "option no-deref\nupdate HEAD %s %s\n", to.Head, from.Head)
	}
	if in.Len() == 0 {
		return nil
	}
	_, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, Stdin: strings.NewReader(in.String()),
		Args: []string{"update-ref", "-m", "ramus undo", "--stdin"}})
	return err
}

// resetMode is "soft" or "mixed" for a reset that left the files, or
// the index and the files, as they were; "" for any other write.
func resetMode(op Op) string {
	if len(op.Cmd.Args) == 0 || op.Cmd.Args[0] != "reset" {
		return ""
	}
	for _, a := range op.Cmd.Args[1:] {
		switch a {
		case "--soft":
			return "soft"
		case "--mixed":
			return "mixed"
		case "--":
			return ""
		}
	}
	return ""
}

// partlyError is an undo or redo that moved HEAD and the refs, then
// failed: the entry moves with HEAD, so the other way stays open, and
// the error says what is left.
type partlyError struct {
	err  error
	undo bool
}

func (e *partlyError) Error() string {
	if e.undo {
		return "write: HEAD was put back, but not everything the write changed: " + e.err.Error()
	}
	return "write: HEAD was moved again, but not everything was redone: " + e.err.Error()
}

func (e *partlyError) Unwrap() error { return e.err }

// restoreFiles puts the paths of e's snapshot back exactly as they were:
// index, work tree, and files that were absent; only those the write
// touched, never the whole work tree.
func restoreFiles(ctx context.Context, q *Queue, w snapshot.Where, e Entry) error {
	if e.Snapshot.Commit == "" {
		return errors.New("write: the write kept no snapshot to undo from")
	}
	if e.unknown {
		return errUnknownPaths
	}
	if len(e.paths) == 0 {
		return nil // the write dropped nothing the snapshot holds
	}
	_, err := snapshot.Restore(ctx, q.r, w, e.Snapshot, e.paths, snapshot.Exact)
	return err
}

// errUnknownPaths is an undo that cannot tell which files the write
// touched, and so does not put any back.
var errUnknownPaths = errors.New("write: the files the write changed could not be read; its snapshot holds them")

// touchedPaths are the paths an undo of e puts back from its snapshot:
// those the snapshot names; past snapshot.MaxPaths, those the write
// named; for a snapshot of the whole work tree, those it holds work of
// that HEAD did not have (a hard reset, a mixed one), or those the
// write changed (a squash merge). Never the whole work tree, which
// would undo work done since on other files.
func (q *Queue) touchedPaths(ctx context.Context, e Entry) ([]string, error) {
	s := e.Snapshot
	switch {
	case s.Commit == "":
		return nil, nil
	case !s.All:
		return s.Paths, nil
	case e.op.Snapshot != nil && len(e.op.Snapshot.Paths) > 0:
		return e.op.Snapshot.Paths, nil
	}
	index := s.Index
	if index == "" {
		index = s.Commit // the index had conflicts: their files are kept
	}
	set := map[string]bool{}
	list := func(args ...string) error {
		res, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: e.op.Cmd.WorkTree, Read: true, Args: args})
		if err != nil {
			return err
		}
		for _, p := range strings.Split(string(res.Stdout), "\x00") {
			if p != "" {
				set[p] = true
			}
		}
		return nil
	}
	var steps [][]string
	switch {
	case e.Kind == MovesHead && s.Head == "":
		steps = [][]string{{"ls-tree", "-r", "-z", "--name-only", "--full-tree", s.Commit}, {"ls-tree", "-r", "-z", "--name-only", "--full-tree", index}}
	case e.Kind == MovesHead:
		// HEAD's files themselves come back with HEAD.
		steps = [][]string{{"diff-tree", "-r", "-z", "--name-only", s.Head, s.Commit}, {"diff-tree", "-r", "-z", "--name-only", s.Head, index}}
	default:
		steps = [][]string{{"diff-index", "--cached", "-z", "--name-only", index, "--"}, {"diff-index", "-z", "--name-only", s.Commit, "--"}}
	}
	for _, args := range steps {
		if err := list(args...); err != nil {
			return nil, err
		}
	}
	return slices.Sorted(maps.Keys(set)), nil
}

// redoFiles runs a write again, with a fresh snapshot, which it returns;
// ran is whether it came to running the write, if only in part.
func (q *Queue) redoFiles(ctx context.Context, e Entry) (s snapshot.Snapshot, ran bool, err error) {
	cmd := e.op.Cmd
	if e.op.Snapshot != nil {
		w := snapshot.Where{GitDir: q.gitDir, WorkTree: cmd.WorkTree}
		if s, err = snapshot.Take(ctx, q.r, w, *e.op.Snapshot); err != nil {
			return e.Snapshot, false, fmt.Errorf("write: keeping a snapshot first: %w", err)
		}
	}
	cmd.Read = false
	if e.op.Input != nil {
		cmd.Stdin = bytes.NewReader(e.op.Input)
	}
	if _, err := q.r.Run(ctx, &cmd); err != nil {
		return s, true, err
	}
	// A write split over several commands, as a delete of many paths,
	// is redone whole.
	for _, c := range e.op.More {
		c.Read = false
		if _, err := q.r.Run(ctx, &c); err != nil {
			return s, true, err
		}
	}
	return s, true, nil
}

// settleTimeout bounds reading what a write left, once it ran.
const settleTimeout = time.Minute

// settle reads what the write of e left: its branches' settings, and the
// paths its snapshot covers, for undo and redo to check against.
func (q *Queue) settle(e *Entry, config branchConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	if config != nil {
		after, err := q.branchConfig(ctx, e.op.Cmd.WorkTree, slices.Collect(maps.Keys(config)))
		if err == nil {
			e.config = [2]branchConfig{config, after}
		}
	}
	q.settlePaths(ctx, e)
}

// settlePaths reads, once e's write or its redo ran, the paths an undo
// of it puts back, and those paths as the write left them.
func (q *Queue) settlePaths(ctx context.Context, e *Entry) {
	paths, err := q.touchedPaths(ctx, *e)
	e.paths, e.unknown = paths, err != nil
	e.left = nil
	if err == nil {
		e.left = q.leftPaths(ctx, *e, nil)
	}
}

// pathState is the paths a write's snapshot covers, as they are on disk
// and in the index.
type pathState struct {
	files []FileState
	// sums are the files' contents hashed, so that a file written again
	// as it was is not taken for work done since; "" for a folder, a file
	// not there, or one past hashBudget, which its size and time tell.
	sums  []string
	index string // git ls-files --stage of them
}

// hashBudget bounds the bytes of files leftPaths hashes, for a write of
// a large folder.
const hashBudget = 64 << 20

// leftPaths reads the paths an undo of e puts back as they are now, nil
// when it puts none back, or they cannot be read. What git ignores in
// them is left out, as an undo leaves it, unless the snapshot kept it.
// It hashes the files like hashed, or within hashBudget when like is
// nil.
func (q *Queue) leftPaths(ctx context.Context, e Entry, like *pathState) *pathState {
	paths := e.paths
	if len(paths) == 0 {
		return nil
	}
	root := e.op.Cmd.WorkTree
	var files []FileState
	var err error
	if e.Snapshot.Ignored {
		files, err = Observe(root, paths)
	} else {
		files, err = ObserveIgnoring(ctx, q.r, Where{GitDir: q.gitDir, WorkTree: root}, paths)
		files = slices.DeleteFunc(files, func(f FileState) bool { return f.Ignored })
	}
	if err != nil {
		return nil
	}
	sums := make([]string, len(files))
	budget := int64(hashBudget)
	for i, f := range files {
		if like != nil && (i >= len(like.sums) || like.sums[i] == "") {
			continue
		}
		if like != nil && i < len(like.files) && (f.Path != like.files[i].Path || f.Size != like.files[i].Size) {
			// Changed, as its size says: not read, which a file grown
			// past the budget since would be whole.
			sums[i] = "changed"
			continue
		}
		var b []byte
		p := filepath.Join(root, filepath.FromSlash(f.Path))
		switch {
		case !f.Exists || f.Mode.IsDir():
			continue
		case like == nil && f.Size > budget:
			continue
		case f.Mode&os.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(p)
			b = []byte(target)
		default:
			b, err = os.ReadFile(p)
		}
		if err != nil {
			return nil
		}
		budget -= int64(len(b))
		sum := sha256.Sum256(b)
		sums[i] = string(sum[:])
	}
	var index strings.Builder
	for c := range slices.Chunk(paths, 256) {
		res, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: e.op.Cmd.WorkTree, Read: true,
			Env: []string{"GIT_LITERAL_PATHSPECS=1"}, Args: append([]string{"ls-files", "--stage", "-z", "--"}, c...)})
		if err != nil {
			return nil
		}
		index.Write(res.Stdout)
	}
	return &pathState{files: files, sums: sums, index: index.String()}
}

// unchanged refuses to undo e when the paths it puts back changed since
// the write, or its last redo, left them: that would overwrite work done
// since, which no redo brings back. A redo needs no such check: it keeps
// what it drops in a fresh snapshot, which the next undo restores.
func (q *Queue) unchanged(ctx context.Context, e Entry) error {
	if e.Snapshot.Commit == "" {
		return nil
	}
	if e.unknown {
		return errUnknownPaths
	}
	if len(e.paths) == 0 {
		return nil
	}
	errUnread := errors.New("write: the files to put back could not be read; the write's snapshot holds them")
	if e.left == nil {
		return errUnread // as the write left them: what changed since cannot be told
	}
	now := q.leftPaths(ctx, e, e.left)
	if now == nil {
		return errUnread
	}
	if len(now.files) != len(e.left.files) {
		return &StaleError{What: "the files to put back", Saw: strconv.Itoa(len(e.left.files)), Now: strconv.Itoa(len(now.files))}
	}
	for i, f := range e.left.files {
		g := now.files[i]
		changed := g.Path != f.Path || g.Exists != f.Exists || g.Mode != f.Mode || now.sums[i] != e.left.sums[i]
		if e.left.sums[i] == "" && f.Exists && !f.Mode.IsDir() {
			changed = changed || !g.same(f)
		}
		if changed {
			return &StaleError{What: g.Path, Saw: describe(f), Now: describe(g)}
		}
	}
	if now.index != e.left.index {
		return &StaleError{What: "the staged state of " + count(len(e.paths), e.paths[0]), Saw: "as left", Now: "changed"}
	}
	return nil
}

// branchConfig is the settings of branches, branch.<name>.*, by short
// name: what git renames with a branch and drops with it, and so what
// undo puts back. Each holds its keys and values in order, a NUL between
// key and value, as a branch name may hold any other character.
type branchConfig map[string][]string

// branchNames are the short names of the local branches among refs.
func branchNames(refs []string) []string {
	var out []string
	for _, r := range refs {
		if name, ok := strings.CutPrefix(r, "refs/heads/"); ok {
			out = append(out, name)
		}
	}
	return out
}

// branchConfig reads the settings of the branches names in the
// repository's own config, where git branch keeps them.
func (q *Queue) branchConfig(ctx context.Context, workTree string, names []string) (branchConfig, error) {
	res, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: workTree, Read: true, Args: []string{"config", "--local", "-z", "--list"}})
	if err != nil {
		return nil, err
	}
	out := make(branchConfig, len(names))
	for _, n := range names {
		out[n] = nil
	}
	for _, rec := range strings.Split(string(res.Stdout), "\x00") {
		key, value, ok := strings.Cut(rec, "\n")
		if !ok {
			value = "true" // a key with no value reads as true
		}
		rest, ok := strings.CutPrefix(key, "branch.")
		dot := strings.LastIndexByte(rest, '.')
		if !ok || dot < 0 {
			continue
		}
		if name := rest[:dot]; slices.Contains(names, name) {
			out[name] = append(out[name], key+"\x00"+value)
		}
	}
	return out, nil
}

// configError is an undo or redo that moved the refs, but could not
// give the branches their settings back.
type configError struct{ err error }

func (e *configError) Error() string {
	return "write: the refs were put back, but not every branch's settings: " + e.err.Error()
}

func (e *configError) Unwrap() error { return e.err }

// setBranchConfig gives each branch the settings to holds for it, where
// they are as from holds: settings changed since are left as they are.
func (q *Queue) setBranchConfig(ctx context.Context, workTree string, from, to branchConfig) error {
	if len(to) == 0 {
		return nil
	}
	now, err := q.branchConfig(ctx, workTree, slices.Collect(maps.Keys(to)))
	if err != nil {
		return &configError{err}
	}
	git := func(args ...string) error {
		_, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: workTree, Args: append([]string{"config", "--local"}, args...)})
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(to)) {
		if slices.Equal(now[name], to[name]) || !slices.Equal(now[name], from[name]) {
			continue
		}
		if len(now[name]) > 0 {
			if err := git("--remove-section", "branch."+name); err != nil {
				return &configError{err}
			}
		}
		for _, kv := range to[name] {
			k, v, _ := strings.Cut(kv, "\x00")
			if err := git("--add", k, v); err != nil {
				return &configError{err}
			}
		}
	}
	return nil
}
