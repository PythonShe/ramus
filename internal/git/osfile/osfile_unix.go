//go:build unix

package osfile

import (
	"io/fs"
	"os"
	"syscall"
)

// Non-blocking, so that opening a named pipe returns at once (reads of a
// regular file are not affected); no controlling terminal, so that a
// terminal opened by mistake does not become the app's.
func open(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}

func regular(fi fs.FileInfo) bool { return fi.Mode().IsRegular() }

func statName(path string) string { return path }
