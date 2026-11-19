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

// longPath gives an absolute path past MAX_PATH the \\?\ prefix, as
// os.Open does, so that a deep ref or work-tree file opens without
// long paths enabled in the system.
func longPath(path string) string {
	const maxPath = 248 // CreateFile's limit, less room for a file name
	if len(path) < maxPath || !filepath.IsAbs(path) || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	// The prefix turns off the system's own cleaning of the path.
	path = filepath.Clean(path)
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}
