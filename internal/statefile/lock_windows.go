package statefile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes f's lock if no one holds it: busy when someone does, an
// error when the file system has no locks.
func tryLock(f *os.File) (busy bool, err error) {
	var ol windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return true, nil
	}
	return false, err
}

func unlockFile(f *os.File) {
	var ol windows.Overlapped
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}
