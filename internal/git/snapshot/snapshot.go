// Package snapshot keeps copies of uncommitted work before the app does
// anything that can drop it: hidden commits under a private ref of the
// worktree, which git keeps reachable and never pushes, and which expire.
package snapshot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/object"
	"ramus/internal/git/refs"
	"ramus/internal/git/run"
	"ramus/internal/git/tree"
)

// Prefix holds the snapshots. refs/worktree/ is per worktree, so each
// worktree sees its own, and git counts them as reachable when it prunes.
const Prefix = "refs/worktree/ramus/snapshots/"

// MaxPaths is how many paths a snapshot records by name; one of more
// paths is recorded as the whole work tree, which it then covers.
const MaxPaths = 10000

// DefaultKeep is how long a snapshot lives before Expire removes it.
const DefaultKeep = 14 * 24 * time.Hour

// Where names the repository: its git directory and work tree.
type Where struct {
	GitDir, WorkTree string
}

// Options says what to keep.
type Options struct {
	// Reason is what the app was about to do ("Discard changes").
	Reason string
	// Paths are the files at stake, relative to the work tree, taken
	// literally; none means the whole work tree.
	Paths []string
	// Ignored also keeps ignored files, for "Snapshot now" before an
	// agent run.
	Ignored bool
}

// Snapshot is one kept state.
type Snapshot struct {
	Ref    string
	Commit string // hex
	Tree   string // the work tree as kept, hex
	Time   time.Time
	Reason string
	// Head is the commit HEAD was on, empty when unborn.
	Head string
	// Index is a commit whose tree is the index as it was, empty when the
	// index had conflicts and could not be kept.
	Index string
	// Paths are the files recorded; empty when All.
	Paths   []string
	All     bool
	Ignored bool
}

// Take keeps the work tree's state of opts.Paths (or of everything) in a
// new snapshot. It works in a copy of the index, so neither the index nor
// the work tree changes, and stat data in the copy keeps git from hashing
// files that did not change.
func Take(ctx context.Context, r *run.Runner, w Where, opts Options) (Snapshot, error) {
	s := Snapshot{Reason: opts.Reason, Ignored: opts.Ignored, Paths: opts.Paths}
	if len(s.Paths) == 0 || len(s.Paths) > MaxPaths {
		s.Paths, s.All = nil, true
	}
	var err error
	if s.Head, err = revParse(ctx, r, w, "HEAD^{commit}"); err != nil {
		return Snapshot{}, err
	}
	tmp, err := copyIndex(w.GitDir)
	if err != nil {
		return Snapshot{}, err
	}
	defer os.Remove(tmp)
	defer os.Remove(tmp + ".lock")
	env := []string{"GIT_INDEX_FILE=" + tmp}
	git := func(stdin []byte, extra []string, args ...string) (string, error) {
		c := &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Env: append(env, extra...), Args: args}
		if stdin != nil {
			c.Stdin = bytes.NewReader(stdin)
		}
		res, err := r.Run(ctx, c)
		return strings.TrimSpace(string(res.Stdout)), err
	}
	ident := []string{"GIT_AUTHOR_NAME=Ramus", "GIT_AUTHOR_EMAIL=snapshot@ramus.invalid",
		"GIT_COMMITTER_NAME=Ramus", "GIT_COMMITTER_EMAIL=snapshot@ramus.invalid"}
	if indexTree, err := git(nil, nil, "write-tree"); err == nil {
		args := []string{"commit-tree", "--no-gpg-sign", "-m", "Ramus snapshot index", indexTree}
		if s.Head != "" {
			args = append(args, "-p", s.Head)
		}
		if s.Index, err = git(nil, ident, args...); err != nil {
			return Snapshot{}, err
		}
	} else if ctx.Err() != nil {
		return Snapshot{}, err
	}
	if err := clearAssumed(git, s.Paths, s.All); err != nil {
		return Snapshot{}, err
	}
	add := []string{"add", "-A"}
	if opts.Ignored {
		add = append(add, "-f")
	}
	var spec []byte
	if !s.All {
		paths, err := present(ctx, git, w.WorkTree, s.Paths)
		if err != nil {
			return Snapshot{}, err
		}
		if len(paths) == 0 {
			add = nil // nothing on disk or in the index to keep
		} else {
			add = append(add, "--pathspec-from-file=-", "--pathspec-file-nul")
		}
		for _, p := range paths {
			spec = append(append(spec, p...), 0)
		}
	} else {
		add = append(add, "--", ":/")
	}
	lit := []string{"GIT_LITERAL_PATHSPECS=1"}
	if s.All {
		lit = nil
	}
	if add != nil {
		if _, err := git(spec, append(lit, "GIT_NO_LAZY_FETCH=1"), add...); err != nil {
			return Snapshot{}, err
		}
	}
	if s.Tree, err = git(nil, nil, "write-tree"); err != nil {
		return Snapshot{}, err
	}
	args := []string{"commit-tree", "--no-gpg-sign", "-F", "-", s.Tree}
	for _, p := range []string{s.Head, s.Index} {
		if p != "" {
			args = append(args, "-p", p)
		}
	}
	if s.Commit, err = git(message(s), ident, args...); err != nil {
		return Snapshot{}, err
	}
	s.Time = time.Now()
	s.Ref = Prefix + strconv.FormatInt(s.Time.UnixNano(), 10)
	// An empty old value makes git refuse if the name is taken.
	if _, err := git(nil, nil, "update-ref", "-m", "ramus snapshot", s.Ref, s.Commit, ""); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// clearAssumed takes the assume-unchanged mark off entries of the index
// copy, which git add would otherwise skip, keeping the old content while
// the change on disk is about to be dropped. Skip-worktree entries stay:
// in a sparse checkout their files are absent on purpose.
func clearAssumed(git func([]byte, []string, ...string) (string, error), paths []string, all bool) error {
	ls, err := git(nil, nil, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	var marked []byte
	for _, rec := range strings.Split(ls, "\x00") {
		tag, path, ok := strings.Cut(rec, " ")
		if !ok || len(tag) != 1 || tag[0] < 'a' || tag[0] > 'z' {
			continue
		}
		if all || covered(paths, path) {
			marked = append(append(marked, path...), 0)
		}
	}
	if len(marked) == 0 {
		return nil
	}
	_, err = git(marked, nil, "update-index", "--no-assume-unchanged", "-z", "--stdin")
	return err
}

// present drops the paths that are in neither the work tree nor the
// index, which git add refuses and which hold nothing to keep.
func present(ctx context.Context, git func([]byte, []string, ...string) (string, error), root string, paths []string) ([]string, error) {
	var out, missing []string
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			out = append(out, p)
		} else {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	// ls-files takes no pathspec file; chunks keep the arguments short.
	var tracked []string
	for c := range slices.Chunk(missing, 256) {
		ls, err := git(nil, []string{"GIT_LITERAL_PATHSPECS=1"}, append([]string{"ls-files", "-z", "--"}, c...)...)
		if err != nil {
			return nil, err
		}
		tracked = append(tracked, strings.Split(ls, "\x00")...)
	}
	for _, p := range missing {
		if slices.ContainsFunc(tracked, func(t string) bool { return covered([]string{p}, t) }) {
			out = append(out, p)
		}
	}
	return out, nil
}

// copyIndex copies the index next to it, so a split index still finds
// its shared part, and returns the copy's path.
func copyIndex(gitDir string) (string, error) {
	var b [8]byte
	rand.Read(b[:])
	tmp := filepath.Join(gitDir, "ramus-snapshot-"+hex.EncodeToString(b[:])+".index")
	src, err := os.Open(filepath.Join(gitDir, "index"))
	if errors.Is(err, os.ErrNotExist) {
		return tmp, nil // no index yet: git starts an empty one
	}
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func revParse(ctx context.Context, r *run.Runner, w Where, rev string) (string, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, Read: true, Args: []string{"rev-parse", "-q", "--verify", rev}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return "", nil
	}
	return strings.TrimSpace(string(res.Stdout)), err
}

// The message carries what the commit's shape cannot: why it was taken
// and which paths it is about. Paths are quoted so any name survives.
func message(s Snapshot) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Ramus snapshot: %s\n\n", oneLine(s.Reason))
	if s.Head != "" {
		fmt.Fprintf(&b, "ramus-head %s\n", s.Head)
	}
	if s.Index != "" {
		fmt.Fprintf(&b, "ramus-index %s\n", s.Index)
	}
	if s.Ignored {
		b.WriteString("ramus-ignored\n")
	}
	if s.All {
		b.WriteString("ramus-all\n")
	}
	for _, p := range s.Paths {
		fmt.Fprintf(&b, "ramus-path %s\n", strconv.Quote(p))
	}
	return b.Bytes()
}

func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

func parse(s *Snapshot, msg []byte) bool {
	head, body, _ := bytes.Cut(msg, []byte("\n\n"))
	reason, ok := bytes.CutPrefix(head, []byte("Ramus snapshot: "))
	if !ok {
		return false
	}
	s.Reason = string(reason)
	for line := range bytes.Lines(body) {
		line = bytes.TrimSuffix(line, []byte{'\n'})
		key, val, _ := bytes.Cut(line, []byte{' '})
		switch string(key) {
		case "ramus-head":
			s.Head = string(val)
		case "ramus-index":
			s.Index = string(val)
		case "ramus-ignored":
			s.Ignored = true
		case "ramus-all":
			s.All = true
		case "ramus-path":
			if p, err := strconv.Unquote(string(val)); err == nil {
				s.Paths = append(s.Paths, p)
			}
		}
	}
	return true
}

// List returns the worktree's snapshots, newest first.
func List(rd refs.Reader, cat *catfile.Process) ([]Snapshot, error) {
	all, err := rd.List()
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	var oids [][]byte
	for i := range all.Len() {
		name := all.Name(i)
		if strings.HasPrefix(name, Prefix) {
			out = append(out, Snapshot{Ref: name, Commit: hex.EncodeToString(all.OID(i))})
			oids = append(oids, all.OID(i))
		}
	}
	keep := out[:0]
	err = cat.Read(oids, func(i int, o catfile.Object) error {
		if o.Type != "commit" {
			return nil
		}
		c, err := object.ParseCommit(o.Data)
		if err != nil {
			return nil
		}
		s := out[i]
		if !parse(&s, c.Message) {
			return nil
		}
		s.Tree, s.Time = string(c.Tree), time.Unix(c.Committer.When, 0)
		keep = append(keep, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(keep, func(a, b Snapshot) int { return strings.Compare(b.Ref, a.Ref) })
	return keep, nil
}

// File is a file a snapshot holds another state of than its head.
type File struct {
	Path string
	// Status is Added or Modified when the snapshot has the file, Deleted
	// when the file was gone from the work tree.
	Status tree.Status
}

// Files compares a snapshot with the commit it was taken on: the files it
// holds a different state of, limited to its recorded paths.
func Files(cat *catfile.Process, hashSize int, s Snapshot) ([]File, error) {
	var base []byte
	if s.Head != "" {
		id, _ := hex.DecodeString(s.Head)
		err := cat.Read([][]byte{id}, func(_ int, o catfile.Object) error {
			c, err := object.ParseCommit(o.Data)
			if err != nil || o.Type != "commit" {
				return fmt.Errorf("snapshot: head %s unreadable", s.Head)
			}
			base, _ = hex.DecodeString(string(c.Tree))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	snap, _ := hex.DecodeString(s.Tree)
	ch, err := tree.Diff(cat, hashSize, base, snap)
	if err != nil {
		return nil, err
	}
	if ch.Truncated {
		return nil, fmt.Errorf("snapshot: more than %d changed files to list", tree.MaxChanges)
	}
	var out []File
	for i := range ch.Len() {
		p := string(ch.Path(i))
		if s.All || covered(s.Paths, p) {
			out = append(out, File{Path: p, Status: ch.Status(i)})
		}
	}
	return out, nil
}

func covered(paths []string, p string) bool {
	for _, q := range paths {
		if p == q || strings.HasPrefix(p, strings.TrimSuffix(q, "/")+"/") {
			return true
		}
	}
	return false
}

// Mode says how Restore treats what the snapshot does not hold.
type Mode uint8

const (
	// Overlay writes back the files the snapshot holds and leaves every
	// other file, so restoring never deletes: the recovery view.
	Overlay Mode = iota
	// Exact makes the paths as they were when the snapshot was taken: the
	// index from the snapshot's index commit, the work tree from its tree,
	// tracked files it did not have removed. Undo uses it.
	Exact
)

// Restore writes paths back as the snapshot kept them, after keeping the
// current state of those paths in a new snapshot, which it returns.
// Paths may be folders. With Exact and no paths, a snapshot of the whole
// work tree is restored whole.
func Restore(ctx context.Context, r *run.Runner, w Where, s Snapshot, paths []string, mode Mode) (Snapshot, error) {
	whole := len(paths) == 0
	if whole && (mode != Exact || !s.All) {
		return Snapshot{}, errors.New("snapshot: nothing to restore")
	}
	before, err := Take(ctx, r, w, Options{Reason: "Before restoring " + s.Time.Format(time.DateTime), Paths: paths})
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: keeping the current state first: %w", err)
	}
	git := func(spec []string, args ...string) error {
		c := &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Env: []string{"GIT_LITERAL_PATHSPECS=1"}}
		if spec == nil {
			c.Args = append(args, "--", ":/")
			c.Env = nil
		} else {
			var b []byte
			for _, p := range spec {
				b = append(append(b, p...), 0)
			}
			c.Stdin = bytes.NewReader(b)
			c.Args = append(args, "--pathspec-from-file=-", "--pathspec-file-nul")
		}
		_, err := r.Run(ctx, c)
		return err
	}
	// git restore refuses a pathspec that matches nothing in the source
	// (or, without --overlay, the index): such paths hold nothing to do.
	match := func(tree string, index bool) ([]string, error) {
		if whole {
			return nil, nil
		}
		return known(ctx, r, w, tree, index, paths)
	}
	if mode == Overlay {
		ps, err := match(s.Commit, false)
		if err != nil || len(ps) == 0 {
			return before, err
		}
		return before, git(ps, "restore", "--source="+s.Commit, "--worktree", "--overlay")
	}
	if s.Index != "" {
		ps, err := match(s.Index, true)
		if err != nil {
			return before, err
		}
		if whole || len(ps) > 0 {
			if err := git(ps, "restore", "--source="+s.Index, "--staged"); err != nil {
				return before, err
			}
		}
	}
	ps, err := match(s.Commit, true)
	if err != nil {
		return before, err
	}
	if whole || len(ps) > 0 {
		err = git(ps, "restore", "--source="+s.Commit, "--worktree", "--no-overlay")
	}
	return before, err
}

// known returns the paths that name something in tree, or in the index
// when index is set.
func known(ctx context.Context, r *run.Runner, w Where, tree string, index bool, paths []string) ([]string, error) {
	var found []string
	for c := range slices.Chunk(paths, 256) {
		list := func(args ...string) error {
			res, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true,
				Env: []string{"GIT_LITERAL_PATHSPECS=1"}, Args: append(append(args, "--"), c...)})
			if err != nil {
				return err
			}
			for _, p := range strings.Split(string(res.Stdout), "\x00") {
				if p != "" {
					found = append(found, p)
				}
			}
			return nil
		}
		if err := list("ls-tree", "-r", "-z", "--name-only", "--full-tree", tree); err != nil {
			return nil, err
		}
		if index {
			if err := list("ls-files", "-z", "--full-name"); err != nil {
				return nil, err
			}
		}
	}
	var out []string
	for _, p := range paths {
		if slices.ContainsFunc(found, func(f string) bool { return covered([]string{p}, f) }) {
			out = append(out, p)
		}
	}
	return out, nil
}

// Expire removes the snapshots taken before cutoff, in one transaction.
func Expire(ctx context.Context, r *run.Runner, w Where, list []Snapshot, cutoff time.Time) (int, error) {
	var in bytes.Buffer
	n := 0
	for _, s := range list {
		if s.Time.Before(cutoff) {
			fmt.Fprintf(&in, "delete %s %s\n", s.Ref, s.Commit)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	_, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, Stdin: &in, Args: []string{"update-ref", "--stdin"}})
	if err != nil {
		return 0, err
	}
	return n, nil
}
