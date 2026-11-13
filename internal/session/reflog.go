package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/object"
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
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
			Args: []string{"log", "-g", "--no-abbrev", "--date=raw", "--max-count=" + strconv.Itoa(MaxReflog),
				"--format=%H%x00%gd%x00%gs", "--end-of-options", ref, "--"}})
		var out []ReflogEntry
		if err == nil {
			out = parseReflog(res.Stdout, ref)
		}
		update(func() { done(out, err) })
	})
}

// parseReflog reads `git log -g --date=raw --format=%H%x00%gd%x00%gs`:
// with raw dates the selector holds the time, so entries are numbered
// here.
func parseReflog(out []byte, ref string) []ReflogEntry {
	var entries []ReflogEntry
	short := strings.TrimPrefix(ref, "refs/heads/")
	for line := range bytes.SplitSeq(bytes.TrimRight(out, "\n"), []byte{'\n'}) {
		f := bytes.SplitN(line, []byte{0}, 3)
		if len(f) != 3 {
			continue
		}
		id, err := hex.DecodeString(string(f[0]))
		if err != nil {
			continue
		}
		e := ReflogEntry{OID: id, Message: string(f[2]), Selector: short + "@{" + strconv.Itoa(len(entries)) + "}"}
		// f[1] is name@{<seconds> <zone>}.
		if _, sel, ok := bytes.Cut(f[1], []byte("@{")); ok {
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
	r.Life().Go(func(ctx context.Context) {
		lost, err := s.lostCommits(ctx, r.Runner, r.GitDir)
		update(func() { done(lost, err) })
	})
}

func (s *Session) lostCommits(ctx context.Context, rn *run.Runner, gitDir string) ([]LostCommit, error) {
	res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Timeout: FsckTimeout, MaxOutput: 256 << 20,
		Args: []string{"fsck", "--dangling", "--no-reflogs", "--connectivity-only", "--no-progress"}})
	// fsck exits non-zero when it finds damage, and still lists what it
	// found; only a run that listed nothing is an error.
	if err != nil && len(res.Stdout) == 0 {
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
	cat, err := s.Repo.CatFile()
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
			Time: time.Unix(c.Committer.When, 0)})
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
