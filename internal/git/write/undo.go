package write

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
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
}

// History holds a repository's undoable writes, for as long as the
// repository is open.
type History struct {
	mu     sync.Mutex
	done   []Entry
	undone []Entry
}

// MaxHistory bounds the entries kept; the oldest go first.
const MaxHistory = 100

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

func (h *History) push(e Entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.done = append(h.done, e)
	if len(h.done) > MaxHistory {
		h.done = slices.Delete(h.done, 0, len(h.done)-MaxHistory)
	}
	h.undone = nil
}

// ErrNothingToUndo is an Undo or Redo with nothing on its side.
var ErrNothingToUndo = errors.New("write: nothing to undo or redo")

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
func (q *Queue) Undo() *Job { return q.submitHistory("Undo", true) }

// Redo queues redoing the last undone write.
func (q *Queue) Redo() *Job { return q.submitHistory("Redo", false) }

func (q *Queue) submitHistory(name string, undo bool) *Job {
	return q.Submit(Op{Name: name, do: func(ctx context.Context) (run.Result, error) {
		return q.travel(ctx, undo)
	}})
}

func (q *Queue) travel(ctx context.Context, undo bool) (run.Result, error) {
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
	if q.Refs == nil {
		return run.Result{}, errors.New("write: undo needs the repository's refs")
	}
	var err error
	if undo {
		err = q.apply(ctx, e, e.After, e.Before, true)
	} else if e.Kind == FilesOnly || e.op.Snapshot != nil {
		// A write that dropped work is redone by running it again, after
		// checking nothing moved, since only it knows what it drops. Its
		// fresh snapshot is what a later undo restores.
		if err = q.verify(e.Before); err == nil {
			e.Snapshot, err = q.redoFiles(ctx, e)
		}
	} else {
		err = q.apply(ctx, e, e.Before, e.After, false)
	}
	if err != nil {
		return run.Result{}, err
	}
	h.mu.Lock()
	*from = (*from)[:len(*from)-1]
	if undo {
		h.undone = append(h.undone, e)
	} else {
		h.done = append(h.done, e)
	}
	h.mu.Unlock()
	return run.Result{}, nil
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
		_, err := q.r.Run(ctx, &run.Cmd{GitDir: q.gitDir, WorkTree: w.WorkTree, Args: args})
		return err
	}
	switch e.Kind {
	case FilesOnly:
		return restoreFiles(ctx, q, w, e.Snapshot)
	case MovesHead:
		if _, err := snapshot.Take(ctx, q.r, w, snapshot.Options{Reason: "Before " + verb(restore) + " " + e.Name}); err != nil {
			return fmt.Errorf("write: keeping a snapshot first: %w", err)
		}
	}
	// Refs that will exist are set before HEAD moves, so a checkout finds
	// its branch; deletions and the branch HEAD leaves come after.
	early := func(name, nu string) bool { return e.Kind == MovesHead && nu != "" && name != from.Symbolic }
	if err := q.moveRefs(ctx, from, to, early, false); err != nil {
		return err
	}
	skip := ""
	if e.Kind == MovesHead {
		switch {
		case from.Symbolic != to.Symbolic && to.Symbolic != "":
			err = git("checkout", "-q", "--no-guess", strings.TrimPrefix(to.Symbolic, "refs/heads/"), "--")
		case from.Symbolic != to.Symbolic || from.Symbolic == "":
			err = git("checkout", "-q", "--detach", to.Head, "--")
		default:
			err, skip = git("reset", "-q", "--keep", to.Head), to.Symbolic
		}
		if err != nil {
			// Put back what moved early, so the history still matches.
			if rb := q.moveRefs(ctx, to, from, func(name, nu string) bool { return early(name, to.Refs[name]) }, false); rb != nil {
				return fmt.Errorf("%w (and putting refs back failed: %v)", err, rb)
			}
			return err
		}
	}
	late := func(name, nu string) bool { return name != skip && !early(name, nu) }
	if err := q.moveRefs(ctx, from, to, late, e.Kind == RefsOnly); err != nil {
		return err
	}
	if e.Kind == RefsOnly && from.Symbolic != to.Symbolic && to.Symbolic != "" {
		if err := git("symbolic-ref", "HEAD", to.Symbolic); err != nil {
			return err
		}
	}
	if restore && e.Kind == MovesHead && e.Snapshot.Commit != "" {
		return restoreFiles(ctx, q, w, e.Snapshot)
	}
	return nil
}

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

// restoreFiles puts the paths a write's snapshot covers back exactly as
// they were: index, work tree, and files that were absent.
func restoreFiles(ctx context.Context, q *Queue, w snapshot.Where, s snapshot.Snapshot) error {
	if s.Commit == "" {
		return errors.New("write: the write kept no snapshot to undo from")
	}
	_, err := snapshot.Restore(ctx, q.r, w, s, s.Paths, snapshot.Exact)
	return err
}

// redoFiles runs a write again, with a fresh snapshot, which it returns.
func (q *Queue) redoFiles(ctx context.Context, e Entry) (snapshot.Snapshot, error) {
	cmd := e.op.Cmd
	var s snapshot.Snapshot
	if e.op.Snapshot != nil {
		w := snapshot.Where{GitDir: q.gitDir, WorkTree: cmd.WorkTree}
		var err error
		if s, err = snapshot.Take(ctx, q.r, w, *e.op.Snapshot); err != nil {
			return e.Snapshot, fmt.Errorf("write: keeping a snapshot first: %w", err)
		}
	}
	cmd.Read = false
	if _, err := q.r.Run(ctx, &cmd); err != nil {
		return e.Snapshot, err
	}
	return s, nil
}
