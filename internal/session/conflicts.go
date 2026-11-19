package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ramus/internal/git/conflict"
	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
)

// MergePreview is what merging a commit into HEAD would do, worked out
// without touching the work tree or the index.
type MergePreview struct {
	// BothChanged are the files both sides changed since they split.
	BothChanged []string
	// Conflicts are the files that would not merge cleanly.
	Conflicts []string
	// UpToDate is set when HEAD already has everything.
	UpToDate bool
}

// maxPreviewPaths bounds the paths a preview lists of each kind.
const maxPreviewPaths = 10000

// PreviewMerge works out a merge of what (a branch or hex) into HEAD off
// the main thread with git merge-tree, and hands it to done on it.
func (s *Session) PreviewMerge(what string, done func(MergePreview, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		p, err := previewMerge(ctx, r.Runner, r.GitDir, r.CommonDir, what)
		if ctx.Err() == nil {
			update(func() { done(p, err) })
		}
	})
}

func previewMerge(ctx context.Context, rn *run.Runner, gitDir, commonDir, what string) (MergePreview, error) {
	var p MergePreview
	if what == "" || strings.HasPrefix(what, "-") {
		return p, errors.New("session: nothing to merge")
	}
	gitZEnv := func(env []string, args ...string) ([]string, error) {
		res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, MaxOutput: 64 << 20, Env: env, Args: args})
		var e *run.Error
		if err != nil && !(errors.As(err, &e) && e.ExitCode == 1) {
			return nil, err
		}
		out := strings.Split(strings.TrimRight(string(res.Stdout), "\x00\n"), "\x00")
		if len(out) == 1 && out[0] == "" {
			out = nil
		}
		return out, nil
	}
	gitZ := func(args ...string) ([]string, error) { return gitZEnv(nil, args...) }
	mb, err := gitZ("merge-base", "HEAD", what)
	if err != nil {
		return p, err
	}
	base := ""
	if len(mb) > 0 {
		base = strings.TrimSpace(mb[0])
	}
	if base != "" {
		theirs, err := gitZ("rev-parse", "--verify", "--end-of-options", what+"^{commit}")
		if err != nil {
			return p, err
		}
		if len(theirs) > 0 && strings.TrimSpace(theirs[0]) == base {
			p.UpToDate = true
			return p, nil
		}
		ours, err := gitZ("diff", "--name-only", "-z", "--no-renames", base, "HEAD", "--")
		if err != nil {
			return p, err
		}
		theirsChanged, err := gitZ("diff", "--name-only", "-z", "--no-renames", base, what, "--")
		if err != nil {
			return p, err
		}
		in := map[string]bool{}
		for _, f := range ours {
			in[f] = true
		}
		for _, f := range theirsChanged {
			if in[f] && len(p.BothChanged) < maxPreviewPaths {
				p.BothChanged = append(p.BothChanged, f)
			}
		}
	}
	// merge-tree writes the trees and blobs of the merge: into a folder of
	// their own, the repository's objects read beside it, and removed
	// after, so that the preview, a read, leaves nothing in a repository
	// (one not trusted is promised git only reads).
	objects, err := os.MkdirTemp("", "ramus-preview-*")
	if err != nil {
		return p, err
	}
	defer os.RemoveAll(objects)
	env := []string{"GIT_OBJECT_DIRECTORY=" + objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + alternate(filepath.Join(commonDir, "objects"))}
	// The tree first, then each conflicted path, then an empty entry.
	out, err := gitZEnv(env, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", "--allow-unrelated-histories", "HEAD", what)
	if err != nil {
		return p, err
	}
	for _, f := range out[min(1, len(out)):] {
		if f == "" {
			break
		}
		if len(p.Conflicts) < maxPreviewPaths {
			p.Conflicts = append(p.Conflicts, f)
		}
	}
	slices.Sort(p.BothChanged)
	return p, nil
}

// alternate is dir as one entry of GIT_ALTERNATE_OBJECT_DIRECTORIES:
// quoted, as git reads it, when it holds the list's separator.
func alternate(dir string) string {
	if !strings.ContainsRune(dir, os.PathListSeparator) && !strings.HasPrefix(dir, `"`) {
		return dir
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(dir) + `"`
}

// ConflictFile is a conflicted file as read for the merge editor.
type ConflictFile struct {
	Path string
	// Segments are the file split at its conflict markers; nil for a
	// binary file or one whose markers do not pair up (Err says).
	Segments []conflict.Segment
	Binary   bool
	// Ours and Theirs are false when that side deleted the file.
	Ours, Theirs bool
	// Staged is set when the index holds the file's conflict stages: a
	// file resolved since its status was read has none.
	Staged bool
	// Seen is the file as read, for the write to check it is unchanged.
	Seen write.FileState
	Err  error
}

// LoadConflict reads a conflicted file of the work tree off the main
// thread and hands it to done on it.
func (s *Session) LoadConflict(path string, done func(ConflictFile)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	s.goView(r, func(ctx context.Context) {
		cf := loadConflict(ctx, r.Runner, r.GitDir, r.WorkTree, path)
		if ctx.Err() == nil {
			update(func() { done(cf) })
		}
	})
}

func loadConflict(ctx context.Context, rn *run.Runner, gitDir, workTree, path string) ConflictFile {
	cf := ConflictFile{Path: path}
	res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Env: []string{"GIT_LITERAL_PATHSPECS=1"},
		Args: []string{"ls-files", "-u", "-z", "--", path}})
	if err != nil {
		cf.Err = err
		return cf
	}
	for rec := range bytes.SplitSeq(res.Stdout, []byte{0}) {
		f := bytes.Fields(rec)
		if len(f) >= 3 {
			cf.Staged = true
			switch string(f[2]) {
			case "2":
				cf.Ours = true
			case "3":
				cf.Theirs = true
			}
		}
	}
	seen, err := write.Observe(workTree, []string{path})
	if err != nil {
		cf.Err = err
		return cf
	}
	cf.Seen = seen[0]
	abs, err := pathsafe.Target(workTree, path)
	if err != nil {
		cf.Err = err
		return cf
	}
	// Not a named pipe, whose open would not return.
	f, err := pathsafe.OpenRegular(abs)
	if errors.Is(err, os.ErrNotExist) {
		return cf // deleted on disk: a side is chosen
	}
	if err != nil {
		cf.Err = err
		return cf
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, conflict.MaxSize+1))
	if err != nil {
		cf.Err = err
		return cf
	}
	if len(data) <= conflict.MaxSize {
		// A save is refused whenever the bytes differ from these.
		cf.Seen.Sum, cf.Seen.Size = write.ContentSum(data), int64(len(data))
	}
	// git's rule: a NUL in the first 8,000 bytes is binary.
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		cf.Binary = true
		return cf
	}
	cf.Segments, cf.Err = conflict.Load(ctx, rn, gitDir, workTree, path, data)
	return cf
}
