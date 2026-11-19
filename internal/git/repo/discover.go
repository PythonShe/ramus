// Package repo finds a repository on disk, decides whether the engine's
// own readers can serve it, and owns everything that lives as long as it
// is open.
package repo

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ramus/internal/git/osfile"
	"ramus/internal/git/pathsafe"
)

// Location is where a repository's parts are.
type Location struct {
	// WorkTree is the top of the work tree, empty for a bare repository.
	WorkTree string
	// GitDir holds what is per work tree: HEAD, the index, operation
	// state. For a linked worktree it is under the main repository's
	// worktrees/ directory.
	GitDir string
	// CommonDir holds what all work trees share: objects, refs, config.
	CommonDir string
	Bare      bool
	// Linked is a worktree made by git worktree add.
	Linked bool
	// Submodule is a work tree whose git directory lives in its
	// superproject's modules/ directory.
	Submodule bool
}

// ErrNotFound means no repository contains the path.
var ErrNotFound = errors.New("repo: not a git repository")

// Discover finds the repository that contains path, as git does when run
// there: the nearest .git directory or gitfile above it, or path itself
// when it is a git directory. Its paths are spelled as on disk
// (pathsafe.Real), whatever the letter case path was given in.
func Discover(path string) (Location, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Location{}, err
	}
	if real, err := pathsafe.Real(abs); err == nil {
		abs = real
	}
	for dir := abs; ; {
		if loc, ok, err := at(dir); err != nil || ok {
			return loc, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Location{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		dir = parent
	}
}

func at(dir string) (Location, bool, error) {
	dotgit := filepath.Join(dir, ".git")
	st, err := os.Lstat(dotgit)
	switch {
	case err == nil && st.Mode().IsRegular():
		gitDir, err := readGitfile(dotgit)
		if err != nil {
			return Location{}, false, err
		}
		loc, ok := gitDirLocation(gitDir)
		if !ok {
			return Location{}, false, fmt.Errorf("repo: %s points at %s, which is not a git directory", dotgit, gitDir)
		}
		loc.WorkTree = dir
		loc.Submodule = strings.Contains(filepath.ToSlash(gitDir), "/modules/")
		return loc, true, nil
	case err == nil:
		// A directory, or a symlink to one.
		if loc, ok := gitDirLocation(dotgit); ok {
			loc.WorkTree = dir
			return loc, true, nil
		}
	}
	if loc, ok := gitDirLocation(dir); ok {
		if filepath.Base(dir) == ".git" {
			// Inside a work tree's git directory: the work tree is above.
			loc.WorkTree = filepath.Dir(dir)
			return loc, true, nil
		}
		loc.Bare = true
		return loc, true, nil
	}
	return Location{}, false, nil
}

// smallFile bounds what is read of a file in a git directory that holds
// one line (HEAD, commondir, a gitfile): git's own limit on such a line
// is its path limit, far below this.
const smallFile = 64 << 10

// readGitfile reads "gitdir: <path>" from a .git file.
func readGitfile(path string) (string, error) {
	b, err := osfile.ReadFile(path, smallFile)
	if err != nil {
		return "", err
	}
	line, _, _ := bytes.Cut(b, []byte{'\n'})
	rest, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("gitdir: "))
	if !ok || len(rest) == 0 {
		return "", fmt.Errorf("repo: %s is not a gitfile", path)
	}
	dir := string(rest)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(path), dir)
	}
	return resolve(dir), nil
}

func resolve(p string) string {
	p = filepath.Clean(p)
	if real, err := pathsafe.Real(p); err == nil {
		return real
	}
	return p
}

// gitDirLocation checks dir the way git's is_git_directory does: a HEAD,
// and objects and refs directories, in it or in its common directory.
func gitDirLocation(dir string) (Location, bool) {
	if !validHEAD(filepath.Join(dir, "HEAD")) {
		return Location{}, false
	}
	// Spelled as on disk once, for both: a .git that is a symlink to the
	// git directory must give the common directory its linked worktrees
	// name, not the symlink's path.
	dir = resolve(dir)
	common := dir
	linked := false
	if b, err := osfile.ReadFile(filepath.Join(dir, "commondir"), smallFile); err == nil {
		c := strings.TrimSpace(string(b))
		if !filepath.IsAbs(c) {
			c = filepath.Join(dir, c)
		}
		common, linked = resolve(c), true
	}
	for _, sub := range []string{"objects", "refs"} {
		st, err := os.Stat(filepath.Join(common, sub))
		if err != nil || !st.IsDir() {
			return Location{}, false
		}
	}
	return Location{GitDir: dir, CommonDir: common, Linked: linked && common != dir}, true
}

// validHEAD accepts what git accepts: a symbolic ref, an object id, or
// the old-style symlink into refs/.
func validHEAD(path string) bool {
	st, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		return err == nil && strings.HasPrefix(filepath.ToSlash(target), "refs/")
	}
	// Not a named pipe or a device: git's validate_headref refuses those
	// too, and a read of one would not return.
	b, err := osfile.ReadFile(path, smallFile)
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "ref: refs/") {
		return true
	}
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
