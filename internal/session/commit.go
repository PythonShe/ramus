package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
			_, cs.Last, _ = message.Last(ctx, r.Runner, w)
		}
		s.handOver(r, func() { done(cs) }, func() { done(CommitSetup{Comment: "#", Err: ErrReopened}) })
	})
}

// HeadMessage reads the message of the commit HEAD is on now, off the
// main thread, for an amend, and hands it to done on it.
func (s *Session) HeadMessage(done func(id, msg string, err error)) {
	r := s.Repo
	if r == nil {
		done("", "", context.Canceled)
		return
	}
	w := message.Where{GitDir: r.GitDir, WorkTree: r.WorkTree}
	r.Life().Go(func(ctx context.Context) {
		id, msg, err := message.Last(ctx, r.Runner, w)
		s.handOver(r, func() { done(id, msg, err) }, func() { done("", "", ErrReopened) })
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
	r.Life().Go(func(ctx context.Context) {
		var res LineEnds
		files, err := stagedFiles(ctx, r.Runner, r.GitDir, r.WorkTree, r.Caps.HashSize)
		var cat *catfile.Process
		if err == nil {
			cat, err = r.CatFile()
		}
		if err == nil {
			res.Findings, res.Complete, err = lineend.Check(ctx, cat, files)
		}
		if err == nil && len(res.Findings) > 0 {
			res.AutoCRLF, res.EOL, err = lineend.Settings(ctx, r.Runner, r.GitDir, r.WorkTree, res.Findings)
		}
		res.Err = err
		s.handOver(r, func() { done(res) }, func() { done(LineEnds{Err: ErrReopened}) })
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
	if opt.Seen == "" {
		opt.Seen = hex.EncodeToString(h.OID)
	}
	if !h.Detached {
		opt.Branch = h.Target
	}
	return opt
}

// Commit commits, logged; with push set, a push with those options
// follows a commit that succeeded, from a branch with no upstream to
// the remote push names, else the one remote there is. done, when set, gets the result on the main
// thread: a commit that failed where the user's hooks ran is a
// *HookError.
func (s *Session) Commit(opt write.CommitOptions, push *write.PushOptions, done func(error)) error {
	opt = s.CommitOptions(opt)
	op, err := write.Commit(s.Where(), opt)
	if err != nil {
		return err
	}
	if push != nil && push.Branch == "" {
		// The push sends the branch committed on, or nothing: a switch
		// queued meanwhile would have git push another. Committed with
		// HEAD detached, it refuses once HEAD is on a branch.
		p := push.From(opt.Branch)
		push = &p
	}
	upstream := s.State.Status != nil && s.State.Status.Branch.Upstream != ""
	j, err := s.Submit(op)
	if err != nil {
		return err
	}
	r := s.Repo
	r.Life().Go(func(ctx context.Context) {
		<-j.Done()
		res, err := j.Result()
		committed := err == nil
		if err != nil && !opt.NoVerify && mayBeHooks(res, err) && hooksRun(ctx, r.Runner, r.GitDir, r.WorkTree) {
			err = &HookError{Err: err}
		}
		var pushOpt write.PushOptions
		if push != nil {
			pushOpt = *push
		}
		if err == nil && push != nil && !upstream && pushOpt.Remote == "" {
			// The remote alone is chosen: the caller's other options stand.
			var target write.PushOptions
			target, err = pushTarget(ctx, r.Runner, r.GitDir)
			pushOpt.Remote = target.Remote
		}
		// Only a trusted repository writes, and one never opens again.
		s.handOver(r, func() {
			if err == nil && push != nil {
				_, err = s.Submit(write.Push(s.Where(), pushOpt))
			}
			if committed && err != nil {
				err = &NotPushedError{Err: err}
			}
			if done != nil {
				done(err)
			}
		}, nil)
	})
	return nil
}

// HookError is a commit that failed where hooks of the user's ran,
// which likely failed it; committing without them may go through.
type HookError struct{ Err error }

func (e *HookError) Error() string { return e.Err.Error() }
func (e *HookError) Unwrap() error { return e.Err }

// notHooks are what git says of a commit that failed for a reason of
// its own, on either output, as it exits 1 for a hook that failed too; a
// hook's own words are all it prints for that.
var notHooks = []string{
	"Aborting commit due to empty commit message",
	"nothing to commit", "nothing added to commit", "no changes added to commit",
	"gpg failed to sign", "failed to write commit object",
}

// mayBeHooks reports whether a failed commit may have been stopped by a
// hook: git ran and exited 1, which it does for a hook that failed, and
// said nothing of another reason. A write refused before git ran, a
// lock, a signing failure or one git ends as fatal is none; when unsure,
// it is none, since committing without hooks skips the user's checks.
func mayBeHooks(res run.Result, err error) bool {
	var stale *write.StaleError
	var locked *write.LockedError
	var re *run.Error
	if errors.As(err, &stale) || errors.As(err, &locked) || !errors.As(err, &re) || re.ExitCode != 1 {
		return false
	}
	for _, m := range notHooks {
		if strings.Contains(re.Stderr, m) || bytes.Contains(res.Stdout, []byte(m)) {
			return false
		}
	}
	return true
}

// commitHooks are the hooks a commit runs that can stop it.
var commitHooks = []string{"pre-commit", "prepare-commit-msg", "commit-msg"}

// hooksRun reports whether a commit runs a hook that can stop it: one is
// in the hooks folder, core.hooksPath's or the repository's own.
func hooksRun(ctx context.Context, r *run.Runner, gitDir, workTree string) bool {
	args := []string{"rev-parse"}
	for _, h := range commitHooks {
		args = append(args, "--git-path", "hooks/"+h)
	}
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Untranslated: true, Args: args})
	if err != nil {
		return false
	}
	// git runs in the work tree, or the git directory of a bare
	// repository, and names paths from there.
	dir := workTree
	if dir == "" {
		dir = gitDir
	}
	for _, p := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		fi, err := os.Stat(p)
		// git skips a hook that cannot run, but on Windows, where any
		// file can.
		if err == nil && fi.Mode().IsRegular() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0) {
			return true
		}
	}
	return false
}

// Prepared is what git left for the commit that ends an operation
// stopped part way.
type Prepared struct {
	// Message is the message git prepared (MERGE_MSG), without its
	// comments and the conflicts it lists; "" none.
	Message string
	// Edit is a rebase stopped to edit a commit, which a commit amends;
	// stopped otherwise, a rebase's continue makes the commit.
	Edit bool
}

// LoadPrepared reads what git prepared for the commit of the operation
// in progress, off the main thread, comment being the comment string,
// and hands it to done on it.
func (s *Session) LoadPrepared(comment string, done func(Prepared)) {
	r := s.Repo
	if r == nil {
		done(Prepared{})
		return
	}
	r.Life().Go(func(ctx context.Context) {
		var p Prepared
		p.Message, _ = write.PreparedMessage(r.GitDir, comment)
		_, err := os.Lstat(filepath.Join(r.GitDir, "rebase-merge", "amend"))
		p.Edit = err == nil
		s.handOver(r, func() { done(p) }, func() { done(Prepared{}) })
	})
}

// NotPushedError is a commit made whose push could not start.
type NotPushedError struct{ Err error }

func (e *NotPushedError) Error() string { return "session: committed, not pushed: " + e.Err.Error() }
func (e *NotPushedError) Unwrap() error { return e.Err }

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
		done(signature.Status{}, errNotOpen)
		return
	}
	r.Life().Go(func(ctx context.Context) {
		var st signature.Status
		var err error
		if tag != "" {
			st, err = signature.Tag(ctx, r.Runner, r.GitDir, tag)
		} else {
			st, err = signature.Commit(ctx, r.Runner, r.GitDir, commit)
		}
		s.handOver(r, func() { done(st, err) }, func() { done(signature.Status{}, ErrReopened) })
	})
}
