package session

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/lfs"
	"ramus/internal/git/pathsafe"
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
	// LFS is a file stored in Git LFS: the sides are its content, read
	// from the local LFS store in place of the pointers git keeps, except
	// those NotFetched, whose content was never downloaded.
	LFS        bool
	NotFetched [2]bool
}

// Sides names the whole files a patch was made from: blobs by id, a nil
// or zero id for a side that does not exist, and the work tree's file at
// WorkTreePath, when set, for the new side.
type Sides struct {
	Old, New     []byte
	WorkTreePath string
	// Max bounds each side read whole; 0 is DefaultMaxSide.
	Max int
	// Modes are the old and new sides' modes, which a patch shows and is
	// kept by along with the ids.
	Modes [2]uint32
}

// DefaultMaxSide bounds a whole file read for the view.
const DefaultMaxSide = 32 << 20

// errTooLarge is a side past Sides.Max.
var errTooLarge = errors.New("session: the file is too large to show whole")

// readSides reads both sides into res, or leaves them out: a side too
// large, missing or not a regular file leaves the patch shown alone.
func readSides(ctx context.Context, r *repo.Repository, sd Sides, res *PatchResult) {
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
	old, new, err := diff.BlobPair(ctx, cat, sd.Old, newID)
	if err != nil {
		return
	}
	if sd.WorkTreePath != "" {
		if new, err = readWorkTreeFile(r.WorkTree, sd.WorkTreePath, max); err != nil {
			res.SidesTooLarge = errors.Is(err, errTooLarge)
			return
		}
	}
	sides := [2][]byte{old, new}
	for k, b := range sides {
		p, ok := lfs.ParsePointer(b)
		if !ok {
			continue
		}
		res.LFS = true
		switch content, err := lfs.ReadObject(r.CommonDir, p, int64(max)); {
		case err == nil:
			sides[k] = content
		case errors.Is(err, lfs.ErrTooLarge):
			res.SidesTooLarge = true
			return
		default:
			res.NotFetched[k] = true
		}
	}
	old, new = sides[0], sides[1]
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
	// Regular still: it may have been replaced by a named pipe since.
	f, err := pathsafe.OpenRegular(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(max)+1))
}

// PatchSlot holds the one patch read of a view: a newer read in the slot
// cancels the one before.
type PatchSlot struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	gen    int
}

// take cancels the read the slot holds and holds a new one, under parent.
// latest reports whether that read is still the slot's: a read a newer
// one replaced is not answered, though the repository closes under it.
func (sl *PatchSlot) take(parent context.Context) (ctx context.Context, cancel context.CancelFunc, latest func() bool) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if sl.cancel != nil {
		sl.cancel()
	}
	ctx, cancel = context.WithCancel(parent)
	sl.cancel = cancel
	sl.gen++
	gen := sl.gen
	return ctx, cancel, func() bool {
		sl.mu.Lock()
		defer sl.mu.Unlock()
		return sl.gen == gen
	}
}

// Patch reads a work-tree patch off the main thread, in the form git
// apply takes back, and hands it to done on it. A newer call cancels a
// read still running, whose done is not called; a read whose repository
// closed and opened again (Trust) is answered ErrReopened. A patch read
// before from the same objects and work tree file is handed over at once.
//
// sides names the whole files, for the view to unfold lines from.
func (s *Session) Patch(req diff.PatchRequest, sides Sides, done func(PatchResult)) {
	s.PatchIn(&s.patch, req, sides, done)
}

// PatchIn is Patch in a slot of the caller's, for a view that reads
// patches beside the changes view's.
func (s *Session) PatchIn(slot *PatchSlot, req diff.PatchRequest, sides Sides, done func(PatchResult)) {
	r := s.Repo
	if r == nil {
		done(PatchResult{Err: errors.New("session: the repository is not open")})
		return
	}
	req.ForApply = true
	// git apply takes back no change without lines around it: with a
	// diff.context of 0, every line staged or discarded would fail.
	if cfg := r.Config(); req.Context < 0 && cfg != nil && cfg.Int("diff.context", 3) < 1 {
		req.Context = 3
	}
	req.NoRefresh = s.holding
	ctx, cancel, latest := slot.take(s.viewContext(r))
	shown := s.stillShown()
	key, work := workPatchKey(req, sides)
	if key != "" && !work {
		if a, ok := s.answers().Get(key); ok {
			cancel()
			done(*a.patch)
			return
		}
	}
	file := filepath.Join(r.WorkTree, filepath.FromSlash(sides.WorkTreePath))
	// A newer read, or the view left, cancels it, and is not answered; r
	// closing under it, as Trust opens it again, is.
	reopened := func() {
		if latest() && shown() {
			cancel()
			done(PatchResult{Err: ErrReopened})
		}
	}
	// read runs git, off the main thread; before is the key of the work
	// tree file the patch is kept by, read before git ran.
	read := func(key string, before fileKey) {
		pt, err := diff.WorkTreePatch(ctx, r.Runner, r.GitDir, r.WorkTree, req)
		var res PatchResult
		if err == nil {
			res.Truncated = pt.Truncated
			res.Parsed, err = diff.Parse(pt.Text)
		}
		if err == nil && !res.Truncated {
			readSides(ctx, r, sides, &res)
		}
		res.Err = err
		keep := key != "" && err == nil && ctx.Err() == nil
		if keep && work {
			after, ok := readFileKey(file, time.Now())
			keep = ok && after == before
		}
		if ctx.Err() != nil {
			if closedUnder(r) {
				s.handOver(r, nil, reopened)
			}
			cancel()
			return
		}
		s.handOver(r, func() {
			live := ctx.Err() == nil
			cancel()
			if keep {
				s.keep(r, key, answer{patch: &res})
			}
			if live {
				done(res)
			}
		}, reopened)
	}
	if !work {
		r.Life().Go(func(context.Context) { read(key, fileKey{}) })
		return
	}
	r.Life().Go(func(context.Context) {
		fk, ok := readFileKey(file, time.Now())
		if !ok {
			read("", fileKey{})
			return
		}
		full := key + fk.String()
		s.handOver(r, func() {
			if ctx.Err() != nil {
				cancel()
				return
			}
			if a, ok := s.answers().Get(full); ok {
				cancel()
				done(*a.patch)
				return
			}
			r.Life().Go(func(context.Context) { read(full, fk) })
		}, reopened)
	})
}
