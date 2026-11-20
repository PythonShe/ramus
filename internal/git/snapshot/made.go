package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
)

// Made is what a write made in the paths of the snapshot taken before
// it: the files it left there that were not there when the snapshot was
// taken, kept or ignored. An exact restore of the snapshot removes those
// and nothing else, and each only while it is as the write left it:
// anything else found there then is the user's.
type Made struct {
	// Known is false when what the write made could not be told; an
	// exact restore then removes nothing.
	Known bool
	Files []MadeFile
	// unlisted is a snapshot that did not list the ignored files there
	// before: a file the write left not ignored may have been there,
	// ignored by a rule the write took away, and is removed only while
	// git does not ignore it once the snapshot's files are back.
	unlisted bool
	// untold are files that may be the write's or may have been there,
	// which an exact restore leaves and returns.
	untold []string
}

// MadeFile is a file a write made, as it left it.
type MadeFile struct {
	Path    string
	Mode    fs.FileMode
	Size    int64
	ModTime time.Time
	// Sum is the SHA-256 of its bytes, or of a link's target; "" past
	// madeHashBudget, where its size and time tell.
	Sum string
}

// madeHashBudget bounds the bytes FindMade hashes.
const madeHashBudget = 64 << 20

// FindMade reads, once a write ran after taking s, what it made in
// paths. A snapshot of the whole work tree is of a write that moves
// HEAD or merges into the index, whose files are in the index, which a
// restore takes back itself: nothing is made.
func FindMade(ctx context.Context, r *run.Runner, w Where, s Snapshot, paths []string) (Made, error) {
	m := Made{Known: true}
	if s.Commit == "" || s.All || len(paths) == 0 {
		return m, nil
	}
	m.unlisted = !s.Ignored && !s.listed
	before := pathSet{}
	for _, p := range s.unkept {
		before[strings.TrimSuffix(p, "/")] = true
	}
	files, dirs := onDisk(w.WorkTree, paths)
	var cands []string
	if len(files) > 0 {
		// A file is untracked when the index does not hold it, which git
		// tells without walking the work tree.
		tracked := map[string]bool{}
		if err := listPaths(ctx, r, w, nil, files, func(name string) { tracked[name] = true }, "ls-files", "-z", "--full-name"); err != nil {
			return Made{}, err
		}
		cands = slices.DeleteFunc(files, func(p string) bool { return tracked[p] })
		if m.unlisted {
			ign, err := checkIgnored(ctx, r, w, cands)
			if err != nil {
				return Made{}, err
			}
			cands = slices.DeleteFunc(cands, func(p string) bool { return ign[p] })
		}
	}
	if len(dirs) > 0 {
		var err error
		if cands, err = madeIn(ctx, r, w, s, dirs, before, cands); err != nil {
			return Made{}, err
		}
	}
	for _, p := range s.absent {
		if exists(w.WorkTree, p) {
			cands = append(cands, p)
		}
	}
	if s.listed {
		absent := pathSet{}
		for _, p := range s.absent {
			absent[p] = true
		}
		var inWhole []string
		cands = slices.DeleteFunc(cands, func(p string) bool {
			if absent[p] || !before.holds(p) {
				return false
			}
			if !before[p] {
				inWhole = append(inWhole, p)
			}
			return true
		})
		// A file not ignored now in a folder git ignored whole before:
		// there before, hidden by a rule gone since, or made by the write,
		// which cannot be told. It is left, and said to be.
		ign, err := checkIgnored(ctx, r, w, inWhole)
		if err != nil {
			return Made{}, err
		}
		for _, p := range inWhole {
			if !ign[p] {
				m.untold = append(m.untold, p)
			}
		}
	}
	slices.Sort(cands)
	cands = slices.Compact(cands)
	had, err := known(ctx, r, w, s.Commit, false, cands)
	if err != nil {
		return Made{}, err
	}
	kept := newPathSet(had)
	cands = slices.DeleteFunc(cands, func(p string) bool { return kept[p] })
	if len(cands) > MaxPaths {
		return Made{}, nil
	}
	budget := int64(madeHashBudget)
	for _, p := range cands {
		f, ok := madeFile(w.WorkTree, p, &budget)
		if ok {
			m.Files = append(m.Files, f)
		}
	}
	return m, nil
}

// madeIn appends to cands the files in no index in the folders dirs, of
// a snapshot that kept ignored files, or listed them, ignored ones too.
func madeIn(ctx context.Context, r *run.Runner, w Where, s Snapshot, dirs []string, before pathSet, cands []string) ([]string, error) {
	others := []string{"ls-files", "-z", "--others", "--full-name"}
	if !s.Ignored {
		others = append(others, "--exclude-standard")
	}
	err := listPaths(ctx, r, w, nil, dirs, func(name string) {
		// A folder of its own is a repository inside, which the snapshot
		// leaves out, and so does this.
		if !strings.HasSuffix(name, "/") {
			cands = append(cands, name)
		}
	}, others...)
	if err != nil {
		return nil, err
	}
	if !s.Ignored && s.listed {
		ign, err := listIgnoredIn(ctx, r, w, dirs)
		if err != nil {
			return nil, err
		}
		var whole []string
		for _, e := range ign {
			switch {
			case before.holds(e):
			case strings.HasSuffix(e, "/"):
				whole = append(whole, e)
			default:
				cands = append(cands, e)
			}
		}
		if len(whole) > 0 {
			// A folder git lists whole, as it ignores all in it: one the
			// write made, or one a rule it made hides. Its files one by
			// one, those there before told by the snapshot or the list.
			err := listPaths(ctx, r, w, nil, within(whole, dirs), func(name string) {
				if !strings.HasSuffix(name, "/") {
					cands = append(cands, name)
				}
			}, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--full-name")
			if err != nil {
				return nil, err
			}
		}
	}
	return cands, nil
}

// onDisk splits paths into those of files and links on disk and those
// of folders, leaving out what is not there, or is past a link, where
// git writes nothing.
func onDisk(root string, paths []string) (files, dirs []string) {
	real := map[string]bool{}
	folder := func(d string) bool {
		if d == "." {
			return true
		}
		ok, seen := real[d]
		if !seen {
			fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(d)))
			ok = err == nil && fi.IsDir()
			real[d] = ok
		}
		return ok
	}
	for _, p := range paths {
		p = strings.TrimSuffix(p, "/")
		ok := true
		for d := path.Dir(p); ok && d != "." && d != "/"; d = path.Dir(d) {
			ok = folder(d)
		}
		if !ok {
			continue
		}
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		switch {
		case err != nil:
		case fi.IsDir():
			dirs = append(dirs, p)
		default:
			files = append(files, p)
		}
	}
	return files, dirs
}

// checkIgnored are those of files, in no index, that git ignores.
func checkIgnored(ctx context.Context, r *run.Runner, w Where, files []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(files) == 0 {
		return out, nil
	}
	var in []byte
	for _, f := range files {
		in = append(append(in, f...), 0)
	}
	_, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true, Stdin: bytes.NewReader(in),
		Args: []string{"check-ignore", "-z", "--stdin"}, Stdout: &nulNames{fn: func(name string) { out[name] = true }}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return out, nil // none is
	}
	return out, err
}

// madeFile reads the file at p as the write left it; not ok when it is
// gone already, or not a file or link.
func madeFile(root, p string, budget *int64) (MadeFile, bool) {
	full := filepath.Join(root, filepath.FromSlash(p))
	fi, err := os.Lstat(full)
	if err != nil || !fi.Mode().IsRegular() && fi.Mode()&fs.ModeSymlink == 0 {
		return MadeFile{}, false
	}
	f := MadeFile{Path: p, Mode: fi.Mode(), Size: fi.Size(), ModTime: fi.ModTime()}
	if fi.Mode().IsRegular() && fi.Size() > *budget {
		return f, true
	}
	if b, err := content(full, fi); err == nil {
		*budget -= int64(len(b))
		sum := sha256.Sum256(b)
		f.Sum = string(sum[:])
	}
	return f, true
}

// content is a regular file's bytes, read within the size fi gives, or
// a link's target.
func content(full string, fi fs.FileInfo) ([]byte, error) {
	if fi.Mode()&fs.ModeSymlink != 0 {
		t, err := os.Readlink(full)
		return []byte(t), err
	}
	return pathsafe.ReadFile(full, fi.Size())
}

// asMade reports whether the file f names is still as the write left
// it, in a folder that is one, not a link that leads elsewhere.
func asMade(root string, f MadeFile) bool {
	for dir := path.Dir(f.Path); dir != "." && dir != "/"; dir = path.Dir(dir) {
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	full := filepath.Join(root, filepath.FromSlash(f.Path))
	fi, err := os.Lstat(full)
	if err != nil || fi.Mode().Type() != f.Mode.Type() || fi.Size() != f.Size {
		return false
	}
	if f.Sum == "" {
		return fi.ModTime().Equal(f.ModTime) && fi.Mode() == f.Mode
	}
	b, err := content(full, fi)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(b)
	return string(sum[:]) == f.Sum
}

// removeMade removes the files of made that are still as the write left
// them and still in no index, and returns those it leaves: changed
// since, staged, or, for a snapshot that did not list its ignored files,
// ignored now that the snapshot's files are back, a file that may have
// been there. It never removes a file made does not name. Run once the
// snapshot's files are back.
func removeMade(ctx context.Context, r *run.Runner, w Where, made Made) ([]string, error) {
	var untold []string
	for _, p := range made.untold {
		if exists(w.WorkTree, p) {
			untold = append(untold, p)
		}
	}
	if !made.Known || len(made.Files) == 0 {
		return untold, nil
	}
	names := make([]string, len(made.Files))
	for i, f := range made.Files {
		names[i] = f.Path
	}
	args := []string{"ls-files", "-z", "--others", "--full-name"}
	if made.unlisted {
		args = append(args, "--exclude-standard")
	}
	still := map[string]bool{}
	if err := listPaths(ctx, r, w, nil, names, func(name string) { still[name] = true }, args...); err != nil {
		return nil, err
	}
	var gone, left []string
	for _, f := range made.Files {
		switch {
		case still[f.Path] && asMade(w.WorkTree, f):
			gone = append(gone, f.Path)
		case exists(w.WorkTree, f.Path):
			left = append(left, f.Path)
		}
	}
	return append(left, untold...), removeFiles(w.WorkTree, gone)
}

func exists(root, p string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
	return !errors.Is(err, fs.ErrNotExist)
}

// listIgnored lists the ignored files in paths that git does not track,
// a folder it ignores whole as the folder, with its slash: what a
// snapshot that does not keep ignored files leaves out. Only folders
// are walked.
func listIgnored(ctx context.Context, r *run.Runner, w Where, paths []string) ([]string, error) {
	files, dirs := onDisk(w.WorkTree, paths)
	ign, err := checkIgnored(ctx, r, w, files)
	if err != nil {
		return nil, err
	}
	in, err := listIgnoredIn(ctx, r, w, dirs)
	return append(slices.Sorted(maps.Keys(ign)), in...), err
}

// listIgnoredIn is listIgnored of folders.
func listIgnoredIn(ctx context.Context, r *run.Runner, w Where, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	args := []string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory", "--full-name"}
	var out []string
	collect := &nulNames{fn: func(name string) { out = append(out, name) }}
	c := &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true, Env: []string{"GIT_LITERAL_PATHSPECS=1"}, Stdout: collect}
	if fewPathspecs(paths) {
		c.Args = append(append(slices.Clip(args), "--"), paths...)
		if _, err := r.Run(ctx, c); err == nil {
			return out, nil
		}
		// git refuses pathspecs inside a folder it ignores whole, with an
		// internal error: the whole work tree is listed instead.
		out, collect.buf = nil, nil
	}
	c.Args = args
	if _, err := r.Run(ctx, c); err != nil {
		return nil, err
	}
	set, up := newPathSet(paths), map[string]bool{}
	for _, p := range paths {
		for d := path.Dir(strings.TrimSuffix(p, "/")); d != "." && d != "/"; d = path.Dir(d) {
			up[d] = true
		}
	}
	return slices.DeleteFunc(out, func(name string) bool {
		n := strings.TrimSuffix(name, "/")
		return !set.covers(n) && !(n != name && up[n])
	}), nil
}

// overwritten are the files and folders of paths on disk, ignored as
// unkept lists them, that a write of paths from the tree-ish source
// writes over or removes: a file or folder where it holds a file, an
// ignored file in a folder where it holds a file, and an ignored file
// where it holds a folder. absent are those it holds where nothing is,
// which the write makes, though in a folder ignored whole.
func overwritten(ctx context.Context, r *run.Runner, w Where, source string, paths, unkept []string) (over, absent []string, err error) {
	ign := pathSet{}
	// below are the unkept entries in each folder that holds one.
	below := map[string][]string{}
	for _, p := range unkept {
		p = strings.TrimSuffix(p, "/")
		ign[p] = true
		for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
			below[d] = append(below[d], p)
		}
	}
	var held []string
	// other are the unkept entries where the source holds another kind.
	other := map[string]bool{}
	err = listPaths(ctx, r, w, nil, paths, func(name string) {
		if ign.covers(name) {
			held = append(held, name)
		}
		for _, p := range below[name] {
			other[p] = true
		}
		for d := path.Dir(name); d != "." && d != "/"; d = path.Dir(d) {
			if ign[d] {
				other[d] = true
			}
		}
	}, "ls-tree", "-r", "-z", "--name-only", "--full-tree", source)
	// A folder is kept whole: the write removes all of it.
	files, dirs := onDisk(w.WorkTree, held)
	more, moreDirs := onDisk(w.WorkTree, slices.Sorted(maps.Keys(other)))
	seen := map[string]bool{}
	for _, p := range slices.Concat(files, dirs, more, moreDirs) {
		if !seen[p] {
			seen[p] = true
			over = append(over, p)
		}
	}
	for _, p := range held {
		if !exists(w.WorkTree, p) && len(absent) < MaxPaths {
			absent = append(absent, p)
		}
	}
	return over, absent, err
}

// holds reports whether name, a folder with its slash, is a path of s or
// inside one.
func (s pathSet) holds(name string) bool { return s.covers(strings.TrimSuffix(name, "/")) }

// within is the paths to list the files of folders dirs by, of paths: a
// folder inside one of paths whole, a folder holding one of paths just
// the paths in it.
func within(dirs, paths []string) []string {
	var out []string
	for _, d := range dirs {
		d = strings.TrimSuffix(d, "/")
		inner := false
		for _, p := range paths {
			if p = strings.TrimSuffix(p, "/"); strings.HasPrefix(p, d+"/") {
				out, inner = append(out, p), true
			}
		}
		if !inner {
			out = append(out, d)
		}
	}
	return out
}
