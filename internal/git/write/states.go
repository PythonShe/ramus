package write

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
)

// stateFiles are the files a merge, a squash merge, a cherry-pick, a
// revert or a rebase writes in the git directory for the commands after
// it, and a commit that ends one of them removes: the commits a merge
// takes in and how (MERGE_HEAD, MERGE_MODE), the message the next commit
// starts from (MERGE_MSG, SQUASH_MSG), and rerere's record of the
// conflicts it saw (MERGE_RR). Undo puts each back as it was before the
// write, redo as the write left it. FETCH_HEAD is not one: what a pull
// fetched stays fetched once its merge is undone. rerere's preimages in
// rr-cache are its own cache, which git rerere gc empties.
var stateFiles = []string{"MERGE_HEAD", "MERGE_MODE", "MERGE_MSG", "SQUASH_MSG", "MERGE_RR"}

// stateRefs are the refs of the same kind that the ref store keeps, a
// file in the git directory or a record of a reftable: where HEAD was
// before a reset, merge or rebase (ORIG_HEAD), the tree of a merge's
// conflicts (AUTO_MERGE), and the commit a stopped cherry-pick, revert or
// rebase applies. MERGE_AUTOSTASH is left alone: it holds the changes a
// merge stashed, which nothing else would.
var stateRefs = []string{"ORIG_HEAD", "AUTO_MERGE", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD"}

// maxStateFile bounds a state file kept; a larger one is left as it is.
const maxStateFile = 4 << 20

// stateOf is one state file or ref: its bytes or object id, when there.
type stateOf struct {
	known, there bool
	val          string
}

// gitState is the state files and refs of a git directory, by name; one
// that could not be read is not known, and left as it is.
type gitState map[string]stateOf

// readStates reads the state files and refs of gitDir.
func (q *Queue) readStates(gitDir string) gitState {
	st := gitState{}
	for _, n := range stateFiles {
		b, err := pathsafe.ReadFile(filepath.Join(gitDir, n), maxStateFile)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			st[n] = stateOf{known: true}
		case err == nil:
			st[n] = stateOf{known: true, there: true, val: string(b)}
		}
	}
	for _, n := range stateRefs {
		if oid, err := q.Refs.Resolve(n); err == nil {
			st[n] = stateOf{known: true, there: oid != nil, val: hex.EncodeToString(oid)}
		}
	}
	return st
}

// stateError is an undo or redo that moved the repository but could not
// put back the state files the write left or took.
type stateError struct{ err error }

func (e *stateError) Error() string {
	return "write: the repository moved, but not every state file of git's went back: " + e.err.Error()
}

func (e *stateError) Unwrap() error { return e.err }

// putStates puts the state files and refs e's write changed back as they
// were before it (undo), or as it left them (redo): each that is still as
// the other way left it, so one written since, by a command in a
// terminal, stays.
func (q *Queue) putStates(ctx context.Context, e Entry, undo bool) error {
	from, to := e.states[1], e.states[0]
	if !undo {
		from, to = to, from
	}
	if from == nil || to == nil {
		return nil
	}
	gitDir := e.op.Cmd.GitDir
	now := q.readStates(gitDir)
	changes := func(n string) (f, t stateOf, ok bool) {
		f, t = from[n], to[n]
		return f, t, f.known && t.known && f != t && now[n] == f
	}
	var errs []error
	for _, n := range stateFiles {
		_, t, ok := changes(n)
		if !ok {
			continue
		}
		p := filepath.Join(gitDir, n)
		var err error
		if t.there {
			err = writeState(p, t.val)
		} else if err = os.Remove(p); errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	var in strings.Builder
	for _, n := range stateRefs {
		f, t, ok := changes(n)
		switch {
		case !ok:
		case !t.there:
			fmt.Fprintf(&in, "delete %s %s\n", n, f.val)
		case !f.there:
			fmt.Fprintf(&in, "create %s %s\n", n, t.val)
		default:
			fmt.Fprintf(&in, "update %s %s %s\n", n, t.val, f.val)
		}
	}
	if in.Len() > 0 {
		_, err := q.r.Run(ctx, &run.Cmd{GitDir: gitDir, Stdin: strings.NewReader(in.String()),
			Args: []string{"update-ref", "-m", "ramus " + verb(undo), "--stdin"}})
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return &stateError{err}
	}
	return nil
}

// writeState writes a state file whole, or not at all.
func writeState(p, content string) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".ramus-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
