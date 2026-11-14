package session

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"ramus/internal/git/catfile"
	"ramus/internal/git/diff"
	"ramus/internal/git/lineend"
	"ramus/internal/git/message"
	"ramus/internal/git/run"
	"ramus/internal/git/signature"
	"ramus/internal/git/write"
)

// CommitSetup is what the commit editor starts from.
type CommitSetup struct {
	Template string
	Comment  string
	Recent   []string
	// Last is HEAD's message, for an amend; empty when unborn.
	Last string
	// Encoding is i18n.commitEncoding, for CommitOptions.Encoding.
	Encoding string
	// Sign is commit.gpgSign; CanSign is set when the config names a
	// way to sign, a key or a format, or turns signing on.
	Sign, CanSign bool
	Err           error
}

// RecentMessages is how many recent messages the editor offers.
const RecentMessages = 20

// LoadCommitSetup reads the template, the comment character and recent
// messages off the main thread, and hands them to done on it.
func (s *Session) LoadCommitSetup(done func(CommitSetup)) {
	r := s.Repo
	if r == nil || r.Restricted {
		// A template and messages come from config and hooks of a
		// repository not trusted; nothing is written there anyway.
		done(CommitSetup{Comment: "#"})
		return
	}
	w := message.Where{GitDir: r.GitDir, WorkTree: r.WorkTree}
	update := s.update
	unborn := len(s.State.Head.OID) == 0
	cfg := r.Config()
	r.Life().Go(func(ctx context.Context) {
		var cs CommitSetup
		cs.Sign = cfg.Bool("commit.gpgSign", false)
		_, key := cfg.Get("user.signingKey")
		_, format := cfg.Get("gpg.format")
		cs.CanSign = cs.Sign || key || format
		cs.Template, cs.Err = message.Template(ctx, r.Runner, w)
		cs.Comment = message.CommentChar(ctx, r.Runner, w)
		cs.Encoding = message.Encoding(ctx, r.Runner, w)
		if recent, err := message.Recent(ctx, r.Runner, w, RecentMessages); err == nil {
			cs.Recent = recent
		} else if cs.Err == nil {
			cs.Err = err
		}
		if !unborn {
			cs.Last, _ = message.Last(ctx, r.Runner, w)
		}
		update(func() { done(cs) })
	})
}

// LineEnds is the line-ending check of what is staged.
type LineEnds struct {
	Findings []lineend.Finding
	// Complete is false when there were too many or too large files to
	// check them all.
	Complete      bool
	AutoCRLF, EOL string
	Err           error
}

// CheckLineEnds checks what is staged off the main thread, read from
// the index as the commit will find it, and hands the result to done on
// it.
func (s *Session) CheckLineEnds(done func(LineEnds)) {
	r := s.Repo
	if r == nil {
		done(LineEnds{Complete: true})
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		var res LineEnds
		files, err := stagedFiles(ctx, r.Runner, r.GitDir, r.WorkTree, r.Caps.HashSize)
		var cat *catfile.Process
		if err == nil {
			cat, err = r.CatFile()
		}
		if err == nil {
			res.Findings, res.Complete, err = lineend.Check(cat, files)
		}
		if err == nil && len(res.Findings) > 0 {
			res.AutoCRLF, res.EOL, err = lineend.Settings(ctx, r.Runner, r.GitDir, r.WorkTree, res.Findings)
		}
		res.Err = err
		update(func() { done(res) })
	})
}

// stagedFiles lists the files the index adds or changes against HEAD.
func stagedFiles(ctx context.Context, r *run.Runner, gitDir, workTree string, hashSize int) ([]lineend.Staged, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true,
		Args: []string{"diff", "--cached", "--raw", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", "--ignore-submodules=all"}})
	if err != nil {
		return nil, err
	}
	raw, err := diff.ParseRaw(res.Stdout, hashSize)
	if err != nil {
		return nil, err
	}
	var files []lineend.Staged
	for i := range raw.Len() {
		st := raw.Status(i)
		if st != 'M' && st != 'A' {
			continue
		}
		_, head := raw.Old(i)
		_, index := raw.New(i)
		f := lineend.Staged{Path: string(raw.Path(i)), Index: index}
		if st == 'M' && !zero(head) {
			f.Head = head
		}
		files = append(files, f)
	}
	return files, nil
}

func zero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// CommitOptions fills in what the session knows: the HEAD seen and its
// branch.
func (s *Session) CommitOptions(opt write.CommitOptions) write.CommitOptions {
	h := s.State.Head
	opt.Seen = hex.EncodeToString(h.OID)
	if !h.Detached {
		opt.Branch = h.Target
	}
	return opt
}

// Commit commits, logged; with push set, a push follows a commit that
// succeeded. done, when set, gets the result on the main thread.
func (s *Session) Commit(opt write.CommitOptions, push bool, done func(error)) error {
	op, err := write.Commit(s.Where(), s.CommitOptions(opt))
	if err != nil {
		return err
	}
	upstream := s.State.Status != nil && s.State.Status.Branch.Upstream != ""
	j, err := s.Submit(op)
	if err != nil {
		return err
	}
	r, update := s.Repo, s.update
	r.Life().Go(func(ctx context.Context) {
		<-j.Done()
		_, err := j.Result()
		var pushOpt write.PushOptions
		if err == nil && push && !upstream {
			pushOpt, err = pushTarget(ctx, r.Runner, r.GitDir)
		}
		update(func() {
			if err == nil && push {
				_, err = s.Submit(write.Push(s.Where(), pushOpt))
			}
			if done != nil {
				done(err)
			}
		})
	})
	return nil
}

// ErrNoRemote is a push from a branch with no upstream in a repository
// with no remote, or several and none named origin.
var ErrNoRemote = errors.New("session: this branch has no upstream; add a remote or set one")

// pushTarget is where a branch with no upstream goes: the one remote
// there is, or origin when there are several; the push makes it the
// upstream.
func pushTarget(ctx context.Context, r *run.Runner, gitDir string) (write.PushOptions, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: []string{"remote"}})
	if err != nil {
		return write.PushOptions{}, err
	}
	remotes := strings.Fields(string(res.Stdout))
	switch {
	case len(remotes) == 1:
		return write.PushOptions{Remote: remotes[0]}, nil
	case slices.Contains(remotes, "origin"):
		return write.PushOptions{Remote: "origin"}, nil
	}
	return write.PushOptions{}, ErrNoRemote
}

// Verify asks git about the signature of a commit (hex), or of a tag
// when tag is set (a full ref name), off the main thread, and hands the
// verdict to done on it. git runs the user's gpg or ssh-keygen for it.
func (s *Session) Verify(commit, tag string, done func(signature.Status, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		var st signature.Status
		var err error
		if tag != "" {
			st, err = signature.Tag(ctx, r.Runner, r.GitDir, tag)
		} else {
			st, err = signature.Commit(ctx, r.Runner, r.GitDir, commit)
		}
		update(func() { done(st, err) })
	})
}
