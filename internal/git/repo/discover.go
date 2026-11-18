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
// when it is a git directory.
func Discover(path string) (Location, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Location{}, err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
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

// readGitfile reads "gitdir: <path>" from a .git file.
func readGitfile(path string) (string, error) {
	b, err := os.ReadFile(path)
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
	if real, err := filepath.EvalSymlinks(p); err == nil {
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
	common := dir
	linked := false
	if b, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
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
	dir = resolve(dir)
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
	b, err := os.ReadFile(path)
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
