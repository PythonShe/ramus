package session

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"ramus/internal/git/diff"
	"ramus/internal/git/write"
)

// Compare reads the files changed from commit from to commit to off the
// main thread, renames found, and hands them to done on it as a
// CommitDetail of to whose one parent is from: CommitPatch then reads a
// file's patch between the two. done is called again, Done set, once
// the lines are counted. cancel stops the read; done is not called after
// it. A comparison read before is handed over at once. One whose
// repository closed and opened again (Trust) ends with ErrReopened.
func (s *Session) Compare(from, to []byte, done func(CommitDetail)) (cancel func()) {
	return s.compare(from, to, done)
}

// CompareWorkTree is Compare from commit from to the work tree, through
// git, which reads the work tree: a CommitDetail with WorkTree set and
// no OID, whose files' patches WorkTreeComparePatch reads. The work tree
// changes, so it is read anew each time.
func (s *Session) CompareWorkTree(from []byte, done func(CommitDetail)) (cancel func()) {
	return s.compare(from, nil, done)
}

// compare is Compare, to nil comparing with the work tree.
func (s *Session) compare(from, to []byte, done func(CommitDetail)) (cancel func()) {
	r := s.Repo
	work := to == nil
	if r == nil {
		done(CommitDetail{OID: to, WorkTree: work, Err: errors.New("session: the repository is not open"), Done: true})
		return func() {}
	}
	key := "x\x00" + string(from) + "\x00" + string(to)
	if a, ok := s.answers().Get(key); ok && !work {
		done(*a.detail)
		return func() {}
	}
	ctx, stop := context.WithCancel(s.viewContext(r))
	dropped := false // main thread only
	shown := s.stillShown()
	reopened := func() {
		if !dropped && shown() {
			done(CommitDetail{OID: to, Parents: [][]byte{from}, WorkTree: work, Done: true, Err: ErrReopened})
		}
	}
	cut := func() {
		if closedUnder(r) {
			s.handOver(r, nil, reopened)
		}
	}
	r.Life().Go(func(context.Context) {
		defer stop()
		d := CommitDetail{OID: to, Parents: [][]byte{from}, WorkTree: work}
		if work {
			d.Files, d.Renames, d.Err = diff.WorkTreeRenames(ctx, r.Runner, r.GitDir, r.WorkTree, r.Caps.HashSize, from)
		} else {
			d.Files, d.Renames, d.Err = diff.Renames(ctx, r.Runner, r.GitDir, r.Caps.HashSize, from, to)
		}
		if ctx.Err() != nil {
			cut()
			return
		}
		// The list shows while its lines are counted, which reads every
		// file in it.
		if d.Err == nil {
			listed := d
			s.handOver(r, func() {
				if !dropped {
					done(listed)
				}
			}, nil)
			var ns *diff.NumStat
			var err error
			if work {
				ns, err = diff.WorkTreeBaseLines(ctx, r.Runner, r.GitDir, r.WorkTree, from)
			} else {
				ns, err = diff.CommitLines(ctx, r.Runner, r.GitDir, from, to)
			}
			if err == nil {
				d.Lines = ns.ForRaw(d.Files)
				d.Sum = diff.Total(d.Lines)
			}
			if ctx.Err() != nil {
				cut()
				return
			}
		}
		d.Done = true
		s.handOver(r, func() {
			if d.Err == nil && !work {
				s.keep(r, key, answer{detail: &d})
			}
			if !dropped {
				done(d)
			}
		}, reopened)
	})
	return func() { dropped = true; stop() }
}

// WorkTreeComparePatch reads the patch of paths, the last the new path,
// from commit from to the work tree, whose file is its new side, in
// slot, as PatchIn reads one.
func (s *Session) WorkTreeComparePatch(slot *PatchSlot, from []byte, paths []string, maxBytes int, ws diff.Whitespace, sides Sides, done func(PatchResult)) {
	req := diff.PatchRequest{Source: diff.Head, Base: hex.EncodeToString(from), Paths: paths, Context: -1,
		MaxBytes: maxBytes, Whitespace: ws, Renames: true}
	sides.WorkTreePath = paths[len(paths)-1]
	s.PatchIn(slot, req, sides, done)
}

// Rewrite describes commit d for a rewrite of HEAD's history: the git at
// hand, its first parent, and the local branches the rewrite may move.
func (s *Session) Rewrite(d CommitDetail) write.Rewrite {
	rw := write.Rewrite{Commit: hex.EncodeToString(d.OID)}
	if len(d.Parents) > 0 {
		rw.Parent = hex.EncodeToString(d.Parents[0])
	}
	if r := s.Repo; r != nil {
		rw.Git = r.Runner.Git().Version
	}
	rw.Branches = s.LocalBranches()
	return rw
}

// LocalBranches are the local branches as last read, by full name: what
// a rewrite may move.
func (s *Session) LocalBranches() []string {
	var out []string
	if l := s.State.Refs; l != nil {
		for i := range l.Len() {
			if n := l.Name(i); strings.HasPrefix(n, "refs/heads/") {
				out = append(out, n)
			}
		}
	}
	return out
}
