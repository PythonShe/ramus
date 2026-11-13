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
}

// Sides names the whole files a patch was made from: blobs by id, a nil
// or zero id for a side that does not exist, and the work tree's file at
// WorkTreePath, when set, for the new side.
type Sides struct {
	Old, New     []byte
	WorkTreePath string
}

// maxSide bounds a whole file read for the view.
const maxSide = 32 << 20

// readSides reads both sides, or reports false: a side too large, missing
// or not a regular file leaves the patch shown alone.
func readSides(r *repo.Repository, sd Sides) (old, new []byte, ok bool) {
	if sd.Old == nil && sd.New == nil && sd.WorkTreePath == "" {
		return nil, nil, false
	}
	cat, err := r.CatFile()
	if err != nil {
		return nil, nil, false
	}
	newID := sd.New
	if sd.WorkTreePath != "" {
		newID = nil
	}
	if old, new, err = diff.BlobPair(cat, sd.Old, newID); err != nil {
		return nil, nil, false
	}
	if sd.WorkTreePath != "" {
		if new, err = readWorkTreeFile(r.WorkTree, sd.WorkTreePath); err != nil {
			return nil, nil, false
		}
	}
	return old, new, len(old) <= maxSide && len(new) <= maxSide
}

// readWorkTreeFile reads a regular file of the work tree, not following
// a symbolic link, up to maxSide.
func readWorkTreeFile(workTree, rel string) ([]byte, error) {
	p := filepath.Join(workTree, filepath.FromSlash(rel))
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxSide {
		return nil, errors.New("session: not a file to show whole")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxSide+1))
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
	ctx, cancel := context.WithCancel(r.Life().Context())
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
			res.Old, res.New, res.HasSides = readSides(r, sides)
		}
		res.Err = err
		if ctx.Err() != nil {
			return
		}
		update(func() { done(res) })
	})
}
