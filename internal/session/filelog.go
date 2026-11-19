package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/run"
)

// FileCommit is a commit that changed a file or a folder.
type FileCommit struct {
	OID []byte
	// Path is the file's path in this commit; it differs from the path
	// asked for across a rename.
	Path    string
	Author  string
	Time    time.Time
	Subject string
	// Merges is the number of its parents past the first.
	Merges int
}

// MaxFileLog bounds the commits a file's history lists.
const MaxFileLog = 20000

// FileLogOptions say how a file's history is read.
type FileLogOptions struct {
	// Follow follows the file across renames; only a single file can be.
	Follow bool
	// NoMerges leaves merge commits out.
	NoMerges bool
}

// FileLog reads the commits that changed path (relative to the work tree;
// a folder ends in "/"), newest first, off the main thread, and hands them
// to done on it; limited is set when MaxFileLog cut it short. Leaving the
// view stops the read, and done is not called. A read whose repository
// closed and opened again (Trust) is answered with no commits and no
// error: the view reads again, as for any change of the refs.
func (s *Session) FileLog(path string, opt FileLogOptions, done func(commits []FileCommit, limited bool, err error)) {
	r := s.Repo
	if r == nil {
		done(nil, false, errNotOpen)
		return
	}
	args := []string{"log", "--name-only", "-z", "--format=%x1e%H%x1f%P%x1f%an%x1f%at%x1f%s",
		"--max-count=" + strconv.Itoa(MaxFileLog+1)}
	if opt.Follow && !strings.HasSuffix(path, "/") {
		args = append(args, "--follow")
	}
	if opt.NoMerges {
		args = append(args, "--no-merges")
	}
	args = append(args, "--", path)
	shown := s.stillShown()
	s.goView(r, func(ctx context.Context) {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, MaxOutput: 256 << 20,
			Env: []string{"GIT_LITERAL_PATHSPECS=1"}, Args: args})
		var out []FileCommit
		if err == nil {
			out = parseFileLog(res.Stdout)
		}
		limited := len(out) > MaxFileLog
		if limited {
			out = out[:MaxFileLog]
		}
		if ctx.Err() != nil && !closedUnder(r) {
			return
		}
		s.handOver(r, func() {
			if ctx.Err() == nil {
				done(out, limited, err)
			}
		}, func() {
			if shown() {
				done(nil, false, nil)
			}
		})
	})
}

// parseFileLog reads records of "\x1e" hash, parents, author, time and
// subject split by "\x1f", ending in NUL, then the paths it changed, each
// ending in NUL.
func parseFileLog(out []byte) []FileCommit {
	var commits []FileCommit
	for rec := range bytes.SplitSeq(out, []byte{0x1e}) {
		head, names, ok := bytes.Cut(rec, []byte{0})
		if !ok {
			continue
		}
		f := bytes.SplitN(head, []byte{0x1f}, 5)
		if len(f) != 5 {
			continue
		}
		id, err := hex.DecodeString(string(f[0]))
		if err != nil {
			continue
		}
		when, _ := strconv.ParseInt(string(f[3]), 10, 64)
		c := FileCommit{OID: id, Author: string(f[2]), Time: time.Unix(when, 0), Subject: string(f[4]),
			Merges: bytes.Count(f[1], []byte{' '})}
		names = bytes.TrimLeft(names, "\n")
		if p, _, ok := bytes.Cut(names, []byte{0}); ok {
			c.Path = string(p)
		}
		commits = append(commits, c)
	}
	return commits
}
