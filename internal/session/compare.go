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
// file's patch between the two. cancel stops the read; done is not called
// after it. A comparison read before is handed over at once.
func (s *Session) Compare(from, to []byte, done func(CommitDetail)) (cancel func()) {
	r := s.Repo
	if r == nil {
		done(CommitDetail{OID: to, Err: errors.New("session: the repository is not open"), Done: true})
		return func() {}
	}
	key := "x\x00" + string(from) + "\x00" + string(to)
	if a, ok := s.answers().Get(key); ok {
		done(*a.detail)
		return func() {}
	}
	ctx, stop := context.WithCancel(s.viewContext(r))
	dropped := false // main thread only
	r.Life().Go(func(context.Context) {
		defer stop()
		d := CommitDetail{OID: to, Parents: [][]byte{from}, Done: true}
		d.Files, _, d.Err = diff.Renames(ctx, r.Runner, r.GitDir, r.Caps.HashSize, from, to)
		if ctx.Err() != nil {
			return
		}
		s.update(func() {
			if d.Err == nil {
				s.keep(key, answer{detail: &d})
			}
			if !dropped {
				done(d)
			}
		})
	})
	return func() { dropped = true; stop() }
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
	if l := s.State.Refs; l != nil {
		for i := range l.Len() {
			if n := l.Name(i); strings.HasPrefix(n, "refs/heads/") {
				rw.Branches = append(rw.Branches, n)
			}
		}
	}
	return rw
}
