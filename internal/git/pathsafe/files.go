package pathsafe

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrNotRegular is a path the app expected a file at that holds something
// else: a named pipe, which a read waits on for ever, or a device such as
// /dev/zero, which a read never finishes.
var ErrNotRegular = errors.New("pathsafe: not a regular file")

// ErrTooLarge is a file past the size its reader allows.
var ErrTooLarge = errors.New("pathsafe: file too large")

// OpenRegular opens the file at path for reading, following symlinks, and
// refuses anything but a regular file without waiting on it. A repository
// is the user's to receive from anyone, and a named pipe in place of
// .git/HEAD or a config naming /dev/zero would hold a reader for ever.
func OpenRegular(path string) (*os.File, error) {
	// Non-blocking, so that opening a named pipe returns at once; reads of
	// a regular file are not affected.
	f, err := os.OpenFile(path, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	return f, nil
}

// ReadFile reads the regular file at path whole, refusing one that holds
// more than limit bytes, however its size changes while it is read.
func ReadFile(path string, limit int64) ([]byte, error) {
	f, err := OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > limit {
		return nil, fmt.Errorf("%w: %s", ErrTooLarge, path)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s", ErrTooLarge, path)
	}
	return b, nil
}
