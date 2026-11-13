package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ramus/internal/git/conflict"
	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
)

// BlameCommit is a commit lines are blamed on.
type BlameCommit struct {
	OID     []byte
	Author  string
	Time    time.Time
	Summary string
	// Path is the file's path in that commit.
	Path string
	// Uncommitted is the work tree's own lines.
	Uncommitted bool
}

// BlameGroup is a run of lines, from Start (0-based) on, blamed on one
// commit.
type BlameGroup struct {
	Commit int // index into the commits sent so far
	Start  int
	Lines  int
}

// BlameUpdate is what a blame found since the last one: the new commits
// and groups. Done is set on the last; Err when it failed.
type BlameUpdate struct {
	Commits []BlameCommit
	Groups  []BlameGroup
	Done    bool
	Err     error
}

// MaxBlameSize bounds the files blamed.
const MaxBlameSize = conflict.MaxSize

// blames cancels a blame a newer one replaced: one per repository view.
type blames struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// Blame reads path's text from the work tree and blames its lines with git
// blame --incremental off the main thread, streaming what it finds to
// update on the main thread about every 100 ms. A newer Blame, or cancel,
// stops it.
func (s *Session) Blame(path string, text func([]byte, error), update func(BlameUpdate)) (cancel func()) {
	r := s.Repo
	if r == nil {
		return func() {}
	}
	s.blame.mu.Lock()
	if s.blame.cancel != nil {
		s.blame.cancel()
	}
	ctx, stop := context.WithCancel(r.Life().Context())
	s.blame.cancel = stop
	s.blame.mu.Unlock()
	post := s.update
	r.Life().Go(func(context.Context) {
		defer stop()
		data, err := readWorkFile(r.WorkTree, path)
		if ctx.Err() != nil {
			return
		}
		post(func() { text(data, err) })
		if err != nil {
			return
		}
		pr, pw := io.Pipe()
		var runErr error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, runErr = r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, Stdout: pw,
				Timeout: 10 * time.Minute, Env: []string{"GIT_LITERAL_PATHSPECS=1"},
				Args: []string{"blame", "--incremental", "--", path}})
			pw.CloseWithError(runErr)
		})
		parseErr := parseBlame(ctx, pr, func(u BlameUpdate) {
			if ctx.Err() == nil {
				post(func() { update(u) })
			}
		})
		pr.CloseWithError(errors.New("session: blame ended"))
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		err = runErr
		if err == nil {
			err = parseErr
		}
		post(func() { update(BlameUpdate{Done: true, Err: err}) })
	})
	return stop
}

func readWorkFile(workTree, path string) ([]byte, error) {
	abs, err := pathsafe.Target(workTree, path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBlameSize+1))
	if err == nil && len(data) > MaxBlameSize {
		err = errors.New("session: the file is too large to blame")
	}
	return data, err
}

// parseBlame reads git blame --incremental: for each run of lines a line
// "<commit> <orig> <final> <count>", the commit's headers the first time
// it is named, and "filename <path>" last. It sends what it read about
// every 100 ms.
func parseBlame(ctx context.Context, rd io.Reader, send func(BlameUpdate)) error {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	index := map[string]int{}
	var pending BlameUpdate
	var cur *BlameCommit
	var group BlameGroup
	inGroup := false
	last := time.Now()
	flush := func() {
		if len(pending.Commits) > 0 || len(pending.Groups) > 0 {
			send(pending)
			pending = BlameUpdate{}
		}
		last = time.Now()
	}
	for sc.Scan() {
		line := sc.Text()
		if !inGroup {
			f := strings.Fields(line)
			if len(f) != 4 || len(f[0]) < 40 {
				return errors.New("session: git blame said " + strconv.Quote(line))
			}
			final, err1 := strconv.Atoi(f[2])
			n, err2 := strconv.Atoi(f[3])
			if err1 != nil || err2 != nil || final < 1 {
				return errors.New("session: git blame said " + strconv.Quote(line))
			}
			k, seen := index[f[0]]
			if !seen {
				id, _ := hex.DecodeString(f[0])
				k = len(index)
				index[f[0]] = k
				// A commit's headers all come before its group closes, so
				// no append moves it while cur points at it.
				pending.Commits = append(pending.Commits, BlameCommit{OID: id, Uncommitted: strings.Trim(f[0], "0") == ""})
				cur = &pending.Commits[len(pending.Commits)-1]
			} else {
				cur = nil
			}
			group, inGroup = BlameGroup{Commit: k, Start: final - 1, Lines: n}, true
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch {
		case key == "filename":
			if cur != nil {
				cur.Path = val
			}
			pending.Groups = append(pending.Groups, group)
			inGroup = false
			if time.Since(last) > 100*time.Millisecond {
				flush()
			}
		case cur == nil:
		case key == "author":
			cur.Author = val
		case key == "author-time":
			t, _ := strconv.ParseInt(val, 10, 64)
			cur.Time = time.Unix(t, 0)
		case key == "summary":
			cur.Summary = val
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	flush()
	if err := sc.Err(); err != nil && !bytes.Contains([]byte(err.Error()), []byte("blame ended")) {
		return err
	}
	return nil
}
