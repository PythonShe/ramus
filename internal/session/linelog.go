package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/run"
)

// LineLog is the history of a run of lines: the commits that changed
// them, newest first, and each one's patch of those lines, in one buffer.
type LineLog struct {
	// From and To are the lines (1-based, inclusive) in HEAD's version
	// of the file that were followed back.
	From, To int
	Commits  []FileCommit
	text     []byte
	ends     []int
	// Limited is set when MaxLineLog cut the history short.
	Limited bool
}

// Patch returns commit i's patch of the lines.
func (l *LineLog) Patch(i int) []byte {
	start := 0
	if i > 0 {
		start = l.ends[i-1]
	}
	return l.text[start:l.ends[i]]
}

// MaxLineLog bounds the commits a line history lists: git log -L diffs
// every commit it walks, so a long one costs minutes.
const MaxLineLog = 2000

// ErrNotCommitted is lines that only the work tree has.
var ErrNotCommitted = errors.New("these lines are not committed yet")

// lineMark starts each commit's record in git log -L's output, which
// otherwise holds patches of file content.
const lineMark = "\x1eramus-line-log\x1f"

// LineLog reads the history of lines from to to (1-based, inclusive) of
// path as the work tree has it, off the main thread, and hands it to done
// on it. The lines are first found in HEAD's version of the file: lines
// the work tree changed stand for the lines they replaced.
func (s *Session) LineLog(path string, from, to int, done func(*LineLog, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	s.goView(r, func(ctx context.Context) {
		l, err := readLineLog(ctx, r.Runner, r.GitDir, r.WorkTree, path, from, to)
		if ctx.Err() == nil {
			update(func() { done(l, err) })
		}
	})
}

func readLineLog(ctx context.Context, rn *run.Runner, gitDir, workTree, path string, from, to int) (*LineLog, error) {
	if from < 1 || to < from {
		return nil, fmt.Errorf("session: no lines %d to %d", from, to)
	}
	res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, MaxOutput: 64 << 20,
		Env: []string{"GIT_LITERAL_PATHSPECS=1"}, Config: []string{"core.quotePath=false"},
		Args: []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "-U0", "HEAD", "--", path}})
	if err != nil {
		return nil, err
	}
	p, err := diff.Parse(res.Stdout)
	if err != nil {
		return nil, err
	}
	if len(p.Files) > 0 && p.Files[0].Binary {
		return nil, errors.New("session: a binary file has no lines to follow")
	}
	switch {
	case len(p.Files) > 0 && p.Files[0].Old == diff.Span{}:
		return nil, ErrNotCommitted
	case len(p.Files) == 0:
		// No difference, or a file HEAD does not have at all.
		if _, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true,
			Args: []string{"cat-file", "-e", "HEAD:" + path}}); err != nil {
			return nil, ErrNotCommitted
		}
	}
	lo, hi, ok := toOldLines(p.Hunks, from, to)
	if !ok {
		return nil, ErrNotCommitted
	}
	res, err = rn.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, MaxOutput: 128 << 20, Timeout: 10 * time.Minute,
		Config: []string{"core.quotePath=false"},
		Args: []string{"log", "--no-ext-diff", "--no-color", "--max-count=" + strconv.Itoa(MaxLineLog+1),
			"--format=" + strings.ReplaceAll(lineMark, "\x1e", "%x1e") + "%H%x1f%P%x1f%an%x1f%at%x1f%s",
			"-L" + strconv.Itoa(lo) + "," + strconv.Itoa(hi) + ":" + path, "HEAD"}})
	if errors.Is(err, run.ErrOutputTooLarge) {
		return nil, errors.New("session: the history of these lines is too long to show")
	}
	if err != nil {
		return nil, err
	}
	l := parseLineLog(res.Stdout)
	l.From, l.To = lo, hi
	if len(l.Commits) > MaxLineLog {
		l.Commits, l.ends, l.Limited = l.Commits[:MaxLineLog], l.ends[:MaxLineLog], true
		l.text = l.text[:l.ends[MaxLineLog-1]]
	}
	return l, nil
}

// toOldLines maps lines from to to of a file's new side to its old side,
// through the hunks of a patch made with no context: unchanged lines map
// across, and changed ones to the lines they replaced. ok is false when
// none of them has a line on the old side.
func toOldLines(hunks []diff.Hunk, from, to int) (lo, hi int, ok bool) {
	add := func(n int) {
		if !ok || n < lo {
			lo = n
		}
		if !ok || n > hi {
			hi = n
		}
		ok = true
	}
	shift, next := 0, 1 // old minus new for unchanged lines; first new line not yet walked
	unchanged := func(upTo int) {
		for n := max(next, from); n <= min(upTo, to); n++ {
			add(n + shift)
		}
	}
	for _, h := range hunks {
		ns, nc, os, oc := int(h.NewStart), int(h.NewCount), int(h.OldStart), int(h.OldCount)
		if nc == 0 {
			// A deletion's NewStart is the line before it.
			unchanged(ns)
			next = ns + 1
		} else {
			unchanged(ns - 1)
			next = ns + nc
			if oc > 0 && ns <= to && ns+nc-1 >= from {
				add(os)
				add(os + oc - 1)
			}
		}
		shift += oc - nc
	}
	unchanged(to)
	return lo, hi, ok
}

// parseLineLog reads git log -L's output: each commit's record, starting
// with lineMark, is hash, parents, author, time and subject split by
// "\x1f" on one line, then its patch of the lines.
func parseLineLog(out []byte) *LineLog {
	l := &LineLog{}
	for _, rec := range bytes.Split(out, []byte(lineMark)) {
		head, patch, _ := bytes.Cut(rec, []byte{'\n'})
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
			Merge: bytes.Count(f[1], []byte{' '}) > 0}
		patch = bytes.TrimLeft(patch, "\n")
		if _, after, ok := bytes.Cut(patch, []byte("\n+++ b/")); ok {
			name, _, _ := bytes.Cut(after, []byte{'\n'})
			c.Path = string(name)
		}
		l.Commits = append(l.Commits, c)
		l.text = append(l.text, patch...)
		l.ends = append(l.ends, len(l.text))
	}
	return l
}
