package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/object"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// Seen is HEAD as the user sees it now, for a write that must refuse if
// it moves.
func (s *Session) Seen() write.Seen {
	h := s.State.Head
	seen := write.Seen{Head: hex.EncodeToString(h.OID)}
	if !h.Detached {
		seen.Branch = h.Target
	}
	return seen
}

// ReflogEntry is one move of a ref, newest first.
type ReflogEntry struct {
	OID []byte
	// Selector is git's name for the entry, as HEAD@{2}.
	Selector string
	// Message says what moved it ("commit: fix", "checkout: moving
	// from main to topic").
	Message string
	Time    time.Time
	// Merges is the number of its parents past the first.
	Merges int
}

// MaxReflog bounds the entries a reflog view reads.
const MaxReflog = 10000

// Reflog reads up to MaxReflog entries of ref's reflog off the main
// thread and hands them to done on it.
func (s *Session) Reflog(ref string, done func([]ReflogEntry, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		var out []ReflogEntry
		var err error
		// An entry written between the two readings shifts one against
		// the other; reading again lines them up.
		for range 3 {
			if out, err = readReflog(ctx, r.Runner, r.GitDir, ref); !errors.Is(err, errReflogMoved) {
				break
			}
		}
		update(func() { done(out, err) })
	})
}

// errReflogMoved is a reflog that changed while it was read.
var errReflogMoved = errors.New("session: the reflog changed while it was read")

// readReflog reads ref's reflog twice: with raw dates, whose selectors
// hold each entry's time, and without, whose selectors hold its number.
// git log -g leaves out an entry whose commit is missing, so counting the
// entries listed would number those after it wrong.
func readReflog(ctx context.Context, rn *run.Runner, gitDir, ref string) ([]ReflogEntry, error) {
	logArgs := func(format string, date ...string) []string {
		return append(append([]string{"log", "-g", "--no-abbrev"}, date...), "--max-count="+strconv.Itoa(MaxReflog),
			"--format="+format, "--end-of-options", ref, "--")
	}
	res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: logArgs("%H%x00%P%x00%gd%x00%gs", "--date=raw")})
	if err != nil {
		return nil, err
	}
	out := parseReflog(res.Stdout, ref)
	res, err = rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: logArgs("%H%x00%gd")})
	if err != nil {
		return nil, err
	}
	i := 0
	for line := range bytes.SplitSeq(bytes.TrimRight(res.Stdout, "\n"), []byte{'\n'}) {
		id, gd, ok := bytes.Cut(line, []byte{0})
		if !ok {
			continue
		}
		if i >= len(out) || hex.EncodeToString(out[i].OID) != string(id) {
			return nil, errReflogMoved
		}
		// gd is name@{<n>}.
		if _, sel, ok := bytes.Cut(gd, []byte("@{")); ok {
			if n, err := strconv.Atoi(string(bytes.TrimSuffix(sel, []byte("}")))); err == nil {
				out[i].Selector = strings.TrimPrefix(ref, "refs/heads/") + "@{" + strconv.Itoa(n) + "}"
			}
		}
		i++
	}
	if i != len(out) {
		return nil, errReflogMoved
	}
	return out, nil
}

// parseReflog reads `git log -g --date=raw --format=%H%x00%P%x00%gd%x00%gs`:
// with raw dates the selector holds the time, so entries are numbered
// here by their place, which readReflog then corrects.
func parseReflog(out []byte, ref string) []ReflogEntry {
	var entries []ReflogEntry
	short := strings.TrimPrefix(ref, "refs/heads/")
	for line := range bytes.SplitSeq(bytes.TrimRight(out, "\n"), []byte{'\n'}) {
		f := bytes.SplitN(line, []byte{0}, 4)
		if len(f) != 4 {
			continue
		}
		id, err := hex.DecodeString(string(f[0]))
		if err != nil {
			continue
		}
		e := ReflogEntry{OID: id, Message: string(f[3]), Selector: short + "@{" + strconv.Itoa(len(entries)) + "}",
			Merges: bytes.Count(f[1], []byte{' '})}
		// f[2] is name@{<seconds> <zone>}.
		if _, sel, ok := bytes.Cut(f[2], []byte("@{")); ok {
			secs, _, _ := bytes.Cut(bytes.TrimSuffix(sel, []byte("}")), []byte(" "))
			if n, err := strconv.ParseInt(string(secs), 10, 64); err == nil {
				e.Time = time.Unix(n, 0)
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// LostCommit is a commit no ref or reflog reaches.
type LostCommit struct {
	OID     []byte
	Subject string
	Author  string
	Time    time.Time
	// Merges is the number of its parents past the first.
	Merges int
}

// MaxLost bounds the lost commits listed, newest first.
const MaxLost = 5000

// FsckTimeout bounds the search for lost commits, which reads every
// object in a large repository.
const FsckTimeout = 15 * time.Minute

// LostCommits lists the commits nothing reaches, those reachable only
// from reflogs counted as lost too, off the main thread, and hands them
// to done on it. It reads every object, so it runs only when asked; a
// newer call does not cancel an older one.
func (s *Session) LostCommits(done func([]LostCommit, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	s.goView(r, func(ctx context.Context) {
		lost, err := lostCommits(ctx, r)
		// Not once the view is left: the search was cancelled, and its
		// error would stand over a new search.
		update(func() {
			if ctx.Err() == nil {
				done(lost, err)
			}
		})
	})
}

// lostCommits runs off the main thread, so it is handed the repository
// instead of reading the session's, which only the main thread may.
func lostCommits(ctx context.Context, r *repo.Repository) ([]LostCommit, error) {
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Timeout: FsckTimeout, MaxOutput: 256 << 20,
		Args: []string{"fsck", "--dangling", "--no-reflogs", "--connectivity-only", "--no-progress"}})
	// fsck exits non-zero when it finds damage, and still lists what it
	// found. One stopped, by its time limit, a cancel or too much output,
	// listed only part, which is no answer.
	var e *run.Error
	if err != nil && (len(res.Stdout) == 0 || !errors.As(err, &e) || e.ExitCode < 0) {
		return nil, err
	}
	var ids [][]byte
	for line := range bytes.SplitSeq(res.Stdout, []byte{'\n'}) {
		rest, ok := bytes.CutPrefix(line, []byte("dangling commit "))
		if !ok {
			continue
		}
		if id, err := hex.DecodeString(string(bytes.TrimSpace(rest))); err == nil {
			ids = append(ids, id)
		}
	}
	cat, err := r.CatFile()
	if err != nil {
		return nil, err
	}
	var lost []LostCommit
	err = cat.Read(ids, func(i int, o catfile.Object) error {
		if o.Type != "commit" {
			return nil
		}
		c, err := object.ParseCommit(o.Data)
		if err != nil {
			return nil
		}
		subject, _, _ := bytes.Cut(c.Message, []byte{'\n'})
		lost = append(lost, LostCommit{OID: ids[i], Subject: string(subject), Author: string(c.Author.Name),
			Time: time.Unix(c.Committer.When, 0), Merges: max(c.Parents-1, 0)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(lost, func(a, b LostCommit) int { return b.Time.Compare(a.Time) })
	if len(lost) > MaxLost {
		lost = lost[:MaxLost]
	}
	return lost, nil
}
