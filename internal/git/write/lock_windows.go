package write

import (
	"context"
	"errors"
	"syscall"
)

// errSharingViolation is ERROR_SHARING_VIOLATION, which the syscall
// package does not name.
const errSharingViolation = syscall.Errno(32)

// openHolders tells whether a process has path open by opening it with
// no sharing at all, which Windows refuses while any process holds it
// open, git's own share mode whatever it is. It cannot say which
// process: a held lock is errInUse.
func openHolders(_ context.Context, path string) ([]Holder, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, errSharingViolation) {
		return nil, errInUse
	}
	if err != nil {
		return nil, err
	}
	syscall.CloseHandle(h)
	return nil, nil
}
