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
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gitexe"
	"ramus/internal/git/object"
	"ramus/internal/git/osfile"
	"ramus/internal/git/pathsafe"
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

// filterTimeout bounds a git command that keeps or restores files: it
// runs their clean or smudge filters, Git LFS among them, which take
// their time on large files.
const filterTimeout = 30 * time.Minute

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
	// Worktree, when set, is the folder of another worktree of the
	// repository, about to be removed: its state is kept, and its own
	// snapshots copied, under the refs of the worktree Take is given,
	// since its own go with it. A folder that is gone has no files to
	// keep: Take copies its snapshots and returns none.
	Worktree string
	// Writes says the write that follows writes Paths as they are
	// spelled: Take refuses a path the disk holds under another
	// spelling, on a volume that does not tell the two apart, which the
	// snapshot would not keep and the write would overwrite.
	Writes bool
	// Source, when set, is the tree-ish (hex) the write takes Paths from:
	// the ignored files on disk it holds, which the write overwrites, are
	// kept too.
	Source string
}

// noCommitOut is git add's refusal of a repository inside the work tree
// with no commit checked out.
var noCommitOut = regexp.MustCompile(`error: '([^'\n]+)' does not have a commit checked out`)

// maxLeftOut bounds the repositories without a commit a snapshot leaves
// out, one more each time git add is run again.
const maxLeftOut = 32

// ignoredDirs are the work tree's ignored folders, each with its slash:
// those a rule ignores, not those that only hold ignored files.
func ignoredDirs(git func(stdin []byte, extra []string, args ...string) (string, error)) ([]string, error) {
	out, err := git(nil, nil, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil, err
	}
	var in []byte
	for _, p := range strings.Split(out, "\x00") {
		if strings.HasSuffix(p, "/") {
			in = append(append(in, p...), 0)
		}
	}
	if len(in) == 0 {
		return nil, nil
	}
	out, err = git(in, nil, "check-ignore", "-z", "--stdin")
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return nil, nil // none is
	}
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			dirs = append(dirs, p)
		}
	}
	return dirs, nil
}

// worktreeGitDir is the git directory of the linked worktree at dir, which
// its .git file names.
func worktreeGitDir(dir string) (string, error) {
	b, err := pathsafe.ReadFile(filepath.Join(dir, ".git"), 64<<10)
	if err != nil {
		return "", fmt.Errorf("snapshot: %s is not a linked worktree: %w", dir, err)
	}
	gd, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
	if !ok || gd == "" {
		return "", fmt.Errorf("snapshot: %s has no git directory named in its .git file", dir)
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	return gd, nil
}

// goneWorktreeGitDir is the git directory of the linked worktree whose
// folder, dir, is gone, "" when the repository of gitDir has none there.
// The folder's .git file is gone with it; git's own record of each
// worktree, its gitdir file, names the .git file it had.
func goneWorktreeGitDir(gitDir, dir string) (string, error) {
	want := realParent(dir)
	found := ""
	err := linkedWorktrees(gitDir, func(admin, named string) {
		if found == "" && filepath.Base(named) == ".git" && realParent(filepath.Dir(named)) == want {
			found = admin
		}
	})
	return found, err
}

// linkedWorktrees calls fn with the git directory of each linked worktree
// of the repository of gitDir, and the .git file its gitdir file names:
// "" when that file is missing, empty or cannot be read, a worktree git
// worktree prune takes for gone.
func linkedWorktrees(gitDir string, fn func(admin, named string)) error {
	common := gitDir
	if b, err := pathsafe.ReadFile(filepath.Join(gitDir, "commondir"), 64<<10); err == nil {
		if c := strings.TrimSpace(string(b)); filepath.IsAbs(c) {
			common = c
		} else {
			common = filepath.Join(gitDir, c)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Join(common, "worktrees")
	// Looked at before it is opened: a named pipe there would hold the
	// open until something wrote to it. git takes no such for worktrees.
	if fi, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) || (err == nil && !fi.IsDir()) {
		return nil
	}
	admins, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, a := range admins {
		admin := filepath.Join(common, "worktrees", a.Name())
		if fi, err := os.Stat(admin); err != nil || !fi.IsDir() {
			continue // not a worktree's, and nothing of one's in it
		}
		b, err := pathsafe.ReadFile(filepath.Join(admin, "gitdir"), 64<<10)
		named := strings.TrimSpace(string(b))
		switch {
		case err != nil || named == "":
			named = ""
		case !filepath.IsAbs(named):
			named = filepath.Join(admin, named) // worktree.useRelativePaths
		}
		fn(admin, named)
	}
	return nil
}

// KeepPruned copies into the worktree of w the snapshots of each linked
// worktree, not locked, whose folder is gone or whose gitdir file says
// none: those git worktree prune removes, its snapshots with its git
// directory.
func KeepPruned(ctx context.Context, r *run.Runner, w Where) error {
	var gone []string
	err := linkedWorktrees(w.GitDir, func(admin, named string) {
		if _, err := os.Lstat(filepath.Join(admin, "locked")); err == nil {
			return
		}
		if _, err := os.Lstat(named); named == "" || errors.Is(err, os.ErrNotExist) {
			gone = append(gone, admin)
		}
	})
	if err != nil {
		return err
	}
	for _, admin := range gone {
		if err := adopt(ctx, r, admin, w.GitDir); err != nil {
			return err
		}
	}
	return nil
}

// realParent is path with its parent folder as the system knows it: the
// path itself is gone, the folder it was in may be named another way.
func realParent(path string) string {
	path = filepath.Clean(path)
	if parent, err := pathsafe.Real(filepath.Dir(path)); err == nil {
		return filepath.Join(parent, filepath.Base(path))
	}
	return path
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
	// unkept are, when listed, the ignored files in Paths the snapshot
	// did not keep, a folder ignored whole as the folder with its slash:
	// there before whatever ran next. Only Take lists them, for paths.
	unkept []string
	listed bool
	// absent are files in unkept folders the write takes from its Source
	// where nothing was: the write's once there.
	absent []string
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
	keep := w.GitDir
	if opts.Worktree != "" {
		if _, err := os.Lstat(opts.Worktree); errors.Is(err, os.ErrNotExist) {
			// Its folder was deleted: none of its files are left to keep,
			// and git removes such a worktree as it is. Its snapshots are
			// still in its git directory, until then.
			gd, err := goneWorktreeGitDir(keep, opts.Worktree)
			if err != nil || gd == "" {
				return Snapshot{}, err
			}
			return Snapshot{}, adopt(ctx, r, gd, keep)
		}
		gd, err := worktreeGitDir(opts.Worktree)
		if err != nil {
			return Snapshot{}, err
		}
		w = Where{GitDir: gd, WorkTree: opts.Worktree}
	}
	if opts.Writes && !s.All {
		if err := checkSpelling(w.WorkTree, s.Paths); err != nil {
			return Snapshot{}, err
		}
	}
	var err error
	if s.Head, err = revParse(ctx, r, w, "HEAD^{commit}"); err != nil {
		return Snapshot{}, err
	}
	removeLeftCopies(w.GitDir, time.Now())
	tmp, err := copyIndex(w.GitDir)
	if err != nil {
		return Snapshot{}, err
	}
	defer os.Remove(tmp)
	defer os.Remove(tmp + ".lock")
	env := []string{"GIT_INDEX_FILE=" + tmp}
	// raw is git's output as it printed it, for lists of names, which
	// may start with a space; git trims it, for an object id.
	raw := func(stdin []byte, extra []string, args ...string) (string, error) {
		c := &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Env: append(env, extra...), Args: args, Timeout: filterTimeout, Untranslated: true}
		if stdin != nil {
			c.Stdin = bytes.NewReader(stdin)
		}
		res, err := r.Run(ctx, c)
		return string(res.Stdout), err
	}
	git := func(stdin []byte, extra []string, args ...string) (string, error) {
		out, err := raw(stdin, extra, args...)
		return strings.TrimSpace(out), err
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
	} else if err := foldUnmerged(git); err != nil {
		return Snapshot{}, err
	}
	if err := clearAssumed(git, s.Paths, s.All); err != nil {
		return Snapshot{}, err
	}
	add := []string{"add", "-A"}
	if opts.Ignored {
		add = append(add, "-f")
	}
	if g := r.Git(); g != nil && g.Version.Has(gitexe.AddSparse) {
		// In a sparse checkout, git add refuses a file outside the cone,
		// untracked or ignored, which would fail the snapshot and the
		// write it keeps from.
		add = append(add, "--sparse")
	}
	// Pathspecs go on standard input, each with its magic: literal for
	// the paths asked, excluded for the folders left out.
	var spec []byte
	addSpec := func(magic, p string) { spec = append(append(append(spec, magic...), p...), 0) }
	if !s.All {
		paths, err := present(ctx, raw, w.WorkTree, s.Paths)
		if err != nil {
			return Snapshot{}, err
		}
		if len(paths) == 0 {
			add = nil // nothing on disk or in the index to keep
		}
		for _, p := range paths {
			addSpec(":(literal)", p)
		}
	} else {
		addSpec("", ":/")
		if opts.Ignored && opts.Worktree != "" {
			// A worktree removed keeps its ignored files, a .env, but
			// not its ignored folders, node_modules or a build's output:
			// rebuilt from the rest, and so large that keeping them would
			// hold up the removal for minutes and fill the object store.
			dirs, err := ignoredDirs(raw)
			if err != nil {
				return Snapshot{}, err
			}
			for _, d := range dirs {
				addSpec(":(exclude,literal)", d)
			}
		}
	}
	if add != nil {
		add = append(add, "--pathspec-from-file=-", "--pathspec-file-nul")
		// A repository inside with no commit checked out, one just made
		// with git init, cannot be added: it is left out, and the rest
		// kept, rather than every snapshot failing until it has one.
		for left := map[string]bool{}; ; {
			_, err := git(spec, []string{"GIT_NO_LAZY_FETCH=1"}, add...)
			if err == nil {
				break
			}
			var e *run.Error
			more := false
			if errors.As(err, &e) {
				for _, m := range noCommitOut.FindAllStringSubmatch(e.Stderr, -1) {
					if d := m[1]; !left[d] && len(left) < maxLeftOut {
						left[d] = true
						addSpec(":(exclude,literal)", d)
						more = true
					}
				}
			}
			if !more {
				return Snapshot{}, err
			}
		}
	}
	if !s.All && opts.Worktree == "" {
		// What it leaves out, to tell later what a write made there.
		if opts.Ignored {
			s.listed = true
		} else if un, err := listIgnored(ctx, r, w, s.Paths); err == nil {
			s.unkept, s.listed = un, true
		} else if ctx.Err() != nil {
			return Snapshot{}, err
		}
	}
	if opts.Source != "" && len(s.unkept) > 0 {
		// An ignored file the write writes over, from what it takes the
		// paths from, is kept all the same.
		over, absent, err := overwritten(ctx, r, w, opts.Source, s.Paths, s.unkept)
		if err != nil {
			return Snapshot{}, err
		}
		s.absent = absent
		if len(over) > 0 {
			var force []byte
			for _, p := range over {
				force = append(append(append(force, ":(literal)"...), p...), 0)
			}
			if _, err := git(force, []string{"GIT_NO_LAZY_FETCH=1"}, "add", "-f", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
				return Snapshot{}, err
			}
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
	if _, err := r.Run(ctx, &run.Cmd{GitDir: keep, Args: []string{"update-ref", "-m", "ramus snapshot", s.Ref, s.Commit, ""}}); err != nil {
		return Snapshot{}, err
	}
	if opts.Worktree != "" {
		if err := adopt(ctx, r, w.GitDir, keep); err != nil {
			return Snapshot{}, err
		}
	}
	return s, nil
}

// adopt copies the snapshots of the worktree whose git directory is from
// under the same names in the worktree whose git directory is keep: a
// worktree's refs go with it when it is removed, and its snapshots may
// hold the only copy of work discarded or reset there. A name keep holds
// already, for another commit, takes a suffix.
func adopt(ctx context.Context, r *run.Runner, from, keep string) error {
	theirs, err := snapshotRefs(ctx, r, from)
	if err != nil || len(theirs) == 0 {
		return err
	}
	ours, err := snapshotRefs(ctx, r, keep)
	if err != nil {
		return err
	}
	var in bytes.Buffer
	for _, name := range slices.Sorted(maps.Keys(theirs)) {
		oid, target := theirs[name], name
		for i := 1; ; i++ {
			o, taken := ours[target]
			if !taken {
				break
			}
			if o == oid {
				target = "" // copied already, by a removal that then failed
				break
			}
			target = name + "-" + strconv.Itoa(i)
		}
		if target != "" {
			ours[target] = oid
			fmt.Fprintf(&in, "create %s %s\n", target, oid)
		}
	}
	if in.Len() == 0 {
		return nil
	}
	_, err = r.Run(ctx, &run.Cmd{GitDir: keep, Stdin: &in, Args: []string{"update-ref", "-m", "ramus snapshot", "--stdin"}})
	return err
}

// snapshotRefs are the snapshot refs of the worktree whose git directory
// is gitDir, by name, with the commit each names.
func snapshotRefs(ctx context.Context, r *run.Runner, gitDir string) (map[string]string, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: []string{"for-each-ref", "--format=%(refname) %(objectname)", Prefix}})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for line := range strings.Lines(string(res.Stdout)) {
		if name, oid, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			out[name] = oid
		}
	}
	return out, nil
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
	// ls-files takes no pathspec file: past what a command line holds,
	// the whole index is listed and filtered.
	args := []string{"ls-files", "-z", "--full-name"}
	if fewPathspecs(missing) {
		args = append(append(args, "--"), missing...)
	}
	ls, err := git(nil, []string{"GIT_LITERAL_PATHSPECS=1"}, args...)
	if err != nil {
		return nil, err
	}
	set, found := newPathSet(missing), map[string]bool{}
	for t := range strings.SplitSeq(ls, "\x00") {
		if t != "" {
			set.under(t, func(p string) { found[p] = true })
		}
	}
	for _, p := range missing {
		if found[strings.TrimSuffix(p, "/")] {
			out = append(out, p)
		}
	}
	return out, nil
}

// copyPrefix starts the name of copyIndex's copies.
const copyPrefix = "ramus-snapshot-"

// removeLeftCopies removes the index copies, and git's locks of them,
// that a snapshot left in gitDir when the app ended in the middle of it:
// each is a whole index, and nothing else would ever remove it. Only
// those made longer ago than any snapshot runs are taken, so none in use
// is. A copy's name says when it was made: its modification time is the
// index's.
func removeLeftCopies(gitDir string, now time.Time) {
	for _, pattern := range []string{copyPrefix + "*.index", copyPrefix + "*.index.lock"} {
		names, _ := filepath.Glob(filepath.Join(gitDir, pattern))
		for _, name := range names {
			made, _, _ := strings.Cut(strings.TrimPrefix(filepath.Base(name), copyPrefix), "-")
			at, err := strconv.ParseInt(made, 10, 64)
			if err != nil {
				// Named by a version that did not say when: by the time
				// it was last written, then.
				fi, err := os.Lstat(name)
				if err != nil {
					continue
				}
				at = fi.ModTime().UnixNano()
			}
			if now.Sub(time.Unix(0, at)) > filterTimeout+time.Hour {
				os.Remove(name)
			}
		}
	}
}

// copyIndex copies the index next to it, so a split index still finds
// its shared part, and returns the copy's path.
func copyIndex(gitDir string) (string, error) {
	var b [8]byte
	rand.Read(b[:])
	tmp := filepath.Join(gitDir, copyPrefix+strconv.FormatInt(time.Now().UnixNano(), 10)+"-"+hex.EncodeToString(b[:])+".index")
	src, err := osfile.Open(filepath.Join(gitDir, "index"))
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
	// git trusts an entry's stat data only when the file is older than the
	// index; a copy as new as now would hide a file changed in the same
	// tick as the index was written, and the snapshot would keep the old
	// content.
	st, err := src.Stat()
	if err == nil {
		err = os.Chtimes(tmp, st.ModTime(), st.ModTime())
	}
	if err != nil {
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

// List returns the worktree's snapshots, newest first. rd is nil when the
// repository's refs go to git (repo.Repository.Refs), which happens only
// where git refuses the repository too, so none can be listed.
func List(ctx context.Context, rd refs.Reader, cat *catfile.Process) ([]Snapshot, error) {
	if rd == nil {
		return nil, nil
	}
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
	err = cat.ReadContext(ctx, oids, func(i int, o catfile.Object) error {
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
func Files(ctx context.Context, cat *catfile.Process, hashSize int, s Snapshot) ([]File, error) {
	var base []byte
	if s.Head != "" {
		id, err := hex.DecodeString(s.Head)
		if err != nil || len(id) != hashSize {
			return nil, fmt.Errorf("snapshot: head %q is not an object id", s.Head)
		}
		err = cat.ReadContext(ctx, [][]byte{id}, func(_ int, o catfile.Object) error {
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
	snap, err := hex.DecodeString(s.Tree)
	if err != nil || len(snap) != hashSize {
		return nil, fmt.Errorf("snapshot: tree %q is not an object id", s.Tree)
	}
	ch, err := tree.Diff(ctx, cat, hashSize, base, snap)
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
	// Staged makes the paths' index entries as they were, and leaves the
	// files: undo of a write that changed only the index, as a mixed reset.
	Staged
)

// Restore writes paths back as the snapshot kept them, after keeping the
// current state of those paths in a new snapshot, which it returns.
// Paths may be folders. With Exact and no paths, a snapshot of the whole
// work tree is restored whole. Exact removes no file the index does not
// hold: RestoreExact removes those a write made.
func Restore(ctx context.Context, r *run.Runner, w Where, s Snapshot, paths []string, mode Mode) (Snapshot, error) {
	before, _, err := restore(ctx, r, w, s, paths, mode, Made{})
	return before, err
}

// RestoreExact is Restore with Exact that also removes what the write
// made after s was taken, as FindMade read it, and returns the files of
// made it leaves, changed since or no longer the write's alone.
func RestoreExact(ctx context.Context, r *run.Runner, w Where, s Snapshot, paths []string, made Made) (Snapshot, []string, error) {
	return restore(ctx, r, w, s, paths, Exact, made)
}

func restore(ctx context.Context, r *run.Runner, w Where, s Snapshot, paths []string, mode Mode, made Made) (Snapshot, []string, error) {
	whole := len(paths) == 0
	if whole && (mode != Exact || !s.All) {
		return Snapshot{}, nil, errors.New("snapshot: nothing to restore")
	}
	before, err := Take(ctx, r, w, Options{Reason: "Before restoring " + s.Time.Format(time.DateTime), Paths: paths, Writes: true, Source: s.Commit})
	if err != nil {
		var se *SpellingError
		if errors.As(err, &se) {
			return Snapshot{}, nil, err
		}
		return Snapshot{}, nil, fmt.Errorf("snapshot: keeping the current state first: %w", err)
	}
	git := func(spec []string, args ...string) error {
		// Out of submodules whatever submodule.recurse says: a snapshot
		// holds a submodule's commit, not the files inside it.
		args = append(args, "--no-recurse-submodules")
		c := &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Env: []string{"GIT_LITERAL_PATHSPECS=1"}, Timeout: filterTimeout}
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
			return before, nil, err
		}
		return before, nil, git(ps, "restore", "--source="+s.Commit, "--worktree", "--overlay")
	}
	var ps, left []string
	if mode == Exact {
		// The files first, while the index still has those the write added.
		if ps, err = match(s.Commit, true); err != nil {
			return before, nil, err
		}
		if whole || len(ps) > 0 {
			if err := git(ps, "restore", "--source="+s.Commit, "--worktree", "--no-overlay"); err != nil {
				return before, nil, err
			}
		}
		// Once the snapshot's files are back, so that their .gitignore
		// files say what git ignored then.
		if left, err = removeMade(ctx, r, w, made); err != nil {
			return before, left, err
		}
	}
	if s.Index == "" {
		return before, left, nil
	}
	if ps, err = match(s.Index, true); err != nil {
		return before, left, err
	}
	if whole || len(ps) > 0 {
		err = git(ps, "restore", "--source="+s.Index, "--staged")
	}
	return before, left, err
}

// removeFiles removes files and links under root, then the folders that
// leaves empty, as git does when it removes a file.
func removeFiles(root string, names []string) error {
	for _, name := range names {
		p := filepath.Join(root, filepath.FromSlash(name))
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.IsDir() {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for dir := path.Dir(name); dir != "." && dir != "/"; dir = path.Dir(dir) {
			if os.Remove(filepath.Join(root, filepath.FromSlash(dir))) != nil {
				break // not empty, or gone
			}
		}
	}
	return nil
}

// known returns the paths that name something in tree, or in the index
// when index is set.
func known(ctx context.Context, r *run.Runner, w Where, tree string, index bool, paths []string) ([]string, error) {
	set := newPathSet(paths)
	found := make(map[string]bool, len(set))
	mark := func(name string) { set.under(name, func(p string) { found[p] = true }) }
	if err := listPaths(ctx, r, w, nil, paths, mark, "ls-tree", "-r", "-z", "--name-only", "--full-tree", tree); err != nil {
		return nil, err
	}
	if index {
		if err := listPaths(ctx, r, w, nil, paths, mark, "ls-files", "-z", "--full-name"); err != nil {
			return nil, err
		}
	}
	var out []string
	for _, p := range paths {
		if found[strings.TrimSuffix(p, "/")] {
			out = append(out, p)
		}
	}
	return out, nil
}

// maxPathspecs is how many paths a listing takes as its pathspecs. git
// walks what they have in common, the whole tree when they are spread
// out, so more are listed by one walk of everything, filtered.
const maxPathspecs = 256

// fewPathspecs reports whether paths are few and short enough to be a
// listing's arguments, within the command line every platform takes.
func fewPathspecs(paths []string) bool {
	if len(paths) > maxPathspecs {
		return false
	}
	n := 0
	for _, p := range paths {
		n += run.ArgBytes(p)
	}
	return n <= run.MaxArgBytes
}

// listPaths runs a git listing that prints names relative to the top of
// the work tree, each ended by a NUL, of paths, and calls fn with each
// name paths cover. The names stream past: a listing of a whole large
// tree is never held.
func listPaths(ctx context.Context, r *run.Runner, w Where, env []string, paths []string, fn func(string), args ...string) error {
	if len(paths) == 0 {
		return nil
	}
	var set pathSet
	if fewPathspecs(paths) {
		args = append(append(args, "--"), paths...)
	} else {
		set = newPathSet(paths)
	}
	out := &nulNames{fn: func(name string) {
		if set == nil || set.covers(name) {
			fn(name)
		}
	}}
	_, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true,
		Env: append([]string{"GIT_LITERAL_PATHSPECS=1"}, env...), Args: args, Stdout: out})
	return err
}

// nulNames calls fn with each NUL-ended name written to it.
type nulNames struct {
	buf []byte
	fn  func(string)
}

func (n *nulNames) Write(b []byte) (int, error) {
	n.buf = append(n.buf, b...)
	for {
		i := bytes.IndexByte(n.buf, 0)
		if i < 0 {
			break
		}
		if i > 0 {
			n.fn(string(n.buf[:i]))
		}
		n.buf = n.buf[i+1:]
	}
	return len(b), nil
}

// pathSet holds paths, each without a trailing slash, to tell which of
// them a name is, or is inside of; nil holds none.
type pathSet map[string]bool

func newPathSet(paths []string) pathSet {
	if len(paths) == 0 {
		return nil
	}
	s := make(pathSet, len(paths))
	for _, p := range paths {
		s[strings.TrimSuffix(p, "/")] = true
	}
	return s
}

// under calls fn with each path of s that is name or a folder holding it.
func (s pathSet) under(name string, fn func(string)) {
	for p := name; ; {
		if s[p] {
			fn(p)
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			return
		}
		p = p[:i]
	}
}

// covers reports whether name is a path of s or inside one.
func (s pathSet) covers(name string) bool {
	in := false
	s.under(name, func(string) { in = true })
	return in
}

// Expire removes the snapshots taken before cutoff, in one transaction.
func Expire(ctx context.Context, r *run.Runner, w Where, list []Snapshot, cutoff time.Time) (int, error) {
	var old []Snapshot
	for _, s := range list {
		if s.Time.Before(cutoff) {
			old = append(old, s)
		}
	}
	if err := Delete(ctx, r, w, old); err != nil {
		return 0, err
	}
	return len(old), nil
}

// Delete removes the snapshots in list, in one transaction: all of them,
// or none when one is no longer what was listed.
func Delete(ctx context.Context, r *run.Runner, w Where, list []Snapshot) error {
	if len(list) == 0 {
		return nil
	}
	var in bytes.Buffer
	for _, s := range list {
		if !strings.HasPrefix(s.Ref, Prefix) {
			return fmt.Errorf("snapshot: %q is not a snapshot", s.Ref)
		}
		fmt.Fprintf(&in, "delete %s %s\n", s.Ref, s.Commit)
	}
	_, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, Stdin: &in, Args: []string{"update-ref", "--stdin"}})
	return err
}

// foldUnmerged takes the work tree's version of each path the (copied)
// index holds unmerged, as during a merge that stopped on conflicts: a
// tree cannot be written of an index with conflicts in it. The index
// itself is then not kept; the files, markers and all, are.
func foldUnmerged(git func([]byte, []string, ...string) (string, error)) error {
	out, err := git(nil, nil, "ls-files", "-u", "-z")
	if err != nil || out == "" {
		return err
	}
	var spec []byte
	seen := map[string]bool{}
	for _, rec := range strings.Split(out, "\x00") {
		_, path, ok := strings.Cut(rec, "\t")
		if ok && !seen[path] {
			seen[path] = true
			spec = append(append(spec, path...), 0)
		}
	}
	_, err = git(spec, []string{"GIT_LITERAL_PATHSPECS=1"}, "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}
