// Package osfile opens the files the engine reads itself, in a git
// directory or a work tree, with two guarantees on every platform:
//
//   - git can rename another file over one, or delete it, while it is
//     open here, as it does to commit the index, a ref, packed-refs, a
//     reftable or the commit-graph. Unix allows that always; Windows only
//     when the reader opened the file sharing deletion, which os.Open
//     does not.
//   - only a regular file is read. A repository is the user's to receive
//     from anyone, and a named pipe in place of .git/HEAD would hold a
//     reader for ever, a device such as /dev/zero feed it without end;
//     opening one does not wait on it.
package osfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
)

// ErrNotRegular is a path that holds something other than a regular
// file: a folder, a named pipe, a device or a socket.
var ErrNotRegular = errors.New("not a regular file")

// ErrTooLarge is a file past the size its reader allows.
var ErrTooLarge = errors.New("file too large")

// Open opens the regular file at path for reading, following symlinks.
// Anything else is refused with ErrNotRegular.
func Open(path string) (*os.File, error) {
	f, _, err := openRegular(path)
	return f, err
}

// ReadFile reads the regular file at path whole, refusing with
// ErrTooLarge one that holds more than limit bytes, however its size
// changes while it is read.
func ReadFile(path string, limit int64) ([]byte, error) {
	f, fi, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tooLarge := &fs.PathError{Op: "read", Path: path, Err: ErrTooLarge}
	if fi.Size() > limit {
		return nil, tooLarge
	}
	// One read for a file whose size holds; one byte past it sees EOF.
	size := min(fi.Size(), limit) + 1
	if fi.Size() <= 0 {
		size = min(512, limit+1)
	}
	if int64(int(size)) != size {
		return nil, tooLarge
	}
	b := make([]byte, 0, size)
	r := io.LimitReader(f, limit+1)
	for {
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
	}
	if int64(len(b)) > limit {
		return nil, tooLarge
	}
	return b, nil
}

func openRegular(path string) (*os.File, fs.FileInfo, error) {
	notRegular := &fs.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	// Checked before opening too, so that a device, whose open alone can
	// act on it, is never opened; the check of what was opened is the one
	// that holds.
	if fi, err := os.Stat(path); err != nil {
		return nil, nil, err
	} else if !regular(fi) {
		return nil, nil, notRegular
	}
	f, err := open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !regular(fi) {
		f.Close()
		return nil, nil, notRegular
	}
	return f, fi, nil
}
