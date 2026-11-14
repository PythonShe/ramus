package session

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"ramus/internal/git/diff"
	"ramus/internal/git/repo"
	"ramus/internal/git/write"
)

// Where names the open repository for a write.
func (s *Session) Where() write.Where {
	if s.Repo == nil {
		return write.Where{}
	}
	return write.Where{GitDir: s.Repo.GitDir, WorkTree: s.Repo.WorkTree}
}

// PatchResult is a patch read for the changes view.
type PatchResult struct {
	Parsed    *diff.Parsed
	Truncated bool
	Err       error
	// Old and New are the whole files the patch was made from, when
	// HasSides: the view unfolds the lines around the hunks from them.
	Old, New []byte
	HasSides bool
	// SidesTooLarge is set when a side was past Sides.Max, so the lines
	// around the hunks cannot unfold.
	SidesTooLarge bool
}

// Sides names the whole files a patch was made from: blobs by id, a nil
// or zero id for a side that does not exist, and the work tree's file at
// WorkTreePath, when set, for the new side.
type Sides struct {
	Old, New     []byte
	WorkTreePath string
	// Max bounds each side read whole; 0 is DefaultMaxSide.
	Max int
}

// DefaultMaxSide bounds a whole file read for the view.
const DefaultMaxSide = 32 << 20

// errTooLarge is a side past Sides.Max.
var errTooLarge = errors.New("session: the file is too large to show whole")

// readSides reads both sides into res, or leaves them out: a side too
// large, missing or not a regular file leaves the patch shown alone.
func readSides(r *repo.Repository, sd Sides, res *PatchResult) {
	if sd.Old == nil && sd.New == nil && sd.WorkTreePath == "" {
		return
	}
	max := sd.Max
	if max <= 0 {
		max = DefaultMaxSide
	}
	cat, err := r.CatFile()
	if err != nil {
		return
	}
	newID := sd.New
	if sd.WorkTreePath != "" {
		newID = nil
	}
	old, new, err := diff.BlobPair(cat, sd.Old, newID)
	if err != nil {
		return
	}
	if sd.WorkTreePath != "" {
		if new, err = readWorkTreeFile(r.WorkTree, sd.WorkTreePath, max); err != nil {
			res.SidesTooLarge = errors.Is(err, errTooLarge)
			return
		}
	}
	if len(old) > max || len(new) > max {
		res.SidesTooLarge = true
		return
	}
	res.Old, res.New, res.HasSides = old, new, true
}

// readWorkTreeFile reads a regular file of the work tree, not following
// a symbolic link, up to max bytes.
func readWorkTreeFile(workTree, rel string, max int) ([]byte, error) {
	p := filepath.Join(workTree, filepath.FromSlash(rel))
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("session: not a file to show whole")
	}
	if fi.Size() > int64(max) {
		return nil, errTooLarge
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(max)+1))
}

// patches cancels a patch read that a newer one replaced.
type patches struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// Patch reads a work-tree patch off the main thread, in the form git
// apply takes back, and hands it to done on it. A newer call cancels a
// read still running, whose done is not called.
//
// sides names the whole files, for the view to unfold lines from.
func (s *Session) Patch(req diff.PatchRequest, sides Sides, done func(PatchResult)) {
	r := s.Repo
	if r == nil {
		done(PatchResult{Err: errors.New("session: the repository is not open")})
		return
	}
	req.ForApply = true
	update := s.update
	s.patch.mu.Lock()
	if s.patch.cancel != nil {
		s.patch.cancel()
	}
	ctx, cancel := context.WithCancel(s.viewContext(r))
	s.patch.cancel = cancel
	s.patch.mu.Unlock()
	r.Life().Go(func(context.Context) {
		defer cancel()
		pt, err := diff.WorkTreePatch(ctx, r.Runner, r.GitDir, r.WorkTree, req)
		var res PatchResult
		if err == nil {
			res.Truncated = pt.Truncated
			res.Parsed, err = diff.Parse(pt.Text)
		}
		if err == nil && !res.Truncated {
			readSides(r, sides, &res)
		}
		res.Err = err
		if ctx.Err() != nil {
			return
		}
		update(func() { done(res) })
	})
}
