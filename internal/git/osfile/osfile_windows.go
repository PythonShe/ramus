//go:build windows

package osfile

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func open(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(longPath(path))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	// Backup semantics opens a folder too, so that a folder put in the
	// file's place since the check is refused as not regular, as on Unix,
	// rather than with access denied.
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// regular refuses folders, pipes, devices and sockets only. Go reports
// a file behind a reparse point that is not a link (a OneDrive or other
// cloud placeholder, for one) as irregular, yet it reads as a file does,
// and a repository in a synced folder is common.
func regular(fi fs.FileInfo) bool {
	return fi.Mode()&(fs.ModeDir|fs.ModeNamedPipe|fs.ModeDevice|fs.ModeCharDevice|fs.ModeSocket) == 0
}

// statName is the name the check before opening stats: Go adds no
// prefix to a long path once it has made the process long-path aware,
// which works only where the system turned long paths on.
func statName(path string) string { return longPath(path) }

// longPath gives a path past MAX_PATH the \\?\ prefix (\\?\UNC\ for a
// share), as os.Open does (os/path_windows.go, addExtendedPrefix), so
// that a deep ref or work-tree file opens without long paths turned on
// for the whole system. A relative path is measured as the absolute one
// it names; a device path and one already extended are left as given.
func longPath(path string) string {
	// Room for an 8.3 name past a folder's path: the kernel takes less
	// than 248 bytes unprefixed.
	const maxPath = 248
	sep := os.IsPathSeparator
	if len(path) >= 4 && (path[:4] == `\??\` || sep(path[0]) && sep(path[1]) && path[2] == '?' && sep(path[3])) {
		return path
	}
	abs := filepath.IsAbs(path)
	if abs && len(path) < maxPath {
		return path
	}
	if len(path) >= 4 && sep(path[0]) && sep(path[1]) && path[2] == '.' && sep(path[3]) {
		return path // a device: the prefix would change its meaning
	}
	// The prefix turns off the system's own cleaning of the path (its
	// separators, . and ..), so the path is made full as the system
	// would first.
	p, err := syscall.UTF16FromString(path)
	if err != nil {
		return path
	}
	buf := make([]uint16, len(p)+maxPath)
	for {
		n, err := syscall.GetFullPathName(&p[0], uint32(len(buf)), &buf[0], nil)
		if err != nil {
			return path
		}
		if n < uint32(len(buf)) {
			buf = buf[:n]
			break
		}
		buf = make([]uint16, n)
	}
	full := syscall.UTF16ToString(buf)
	if !abs && len(full) < maxPath {
		return path
	}
	if strings.HasPrefix(full, `\\`) {
		return `\\?\UNC\` + full[2:]
	}
	return `\\?\` + full
}
