package statefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// maxLinks bounds the symbolic links followed to a file, as the systems
// do (40 on Linux, 32 on macOS).
const maxLinks = 40

// ErrLinkLoop is a state file whose symbolic links lead back to one of
// themselves, or past maxLinks.
var ErrLinkLoop = errors.New("its symbolic links make a loop")

// target is the file path names: path itself, or, when it is a symbolic
// link (a file kept with the user's dotfiles and linked into place), the
// file the link leads to, which need not exist yet. Every state file is
// read, written and kept aside as the file a link leads to: replacing
// the link with a file of its own would leave the user's file behind,
// no longer read.
func target(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return path, nil // a missing file is made where it is named
	}
	t, err := follow(path)
	if err != nil {
		return "", err
	}
	// The links are followed by their text, which names the file as the
	// user does; a ".." after a linked folder means another folder to
	// the system, whose answer then wins.
	if a, err := os.Stat(path); err == nil {
		if b, err := os.Stat(t); err != nil || !os.SameFile(a, b) {
			return filepath.EvalSymlinks(path)
		}
	}
	return t, nil
}

// follow follows the links from path by their text to the first name that
// is no link, which need not exist.
func follow(path string) (string, error) {
	for range maxLinks {
		fi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		dest, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(path), dest)
		}
		path = dest
	}
	return "", fmt.Errorf("%s: %w", path, ErrLinkLoop)
}
