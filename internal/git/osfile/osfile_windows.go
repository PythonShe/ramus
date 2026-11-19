//go:build windows

package osfile

import (
	"io/fs"
	"os"
	"syscall"
)

func open(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
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
