//go:build !windows

package statefile

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes f's lock if no one holds it: busy when someone does, an
// error when the file system has no locks.
func tryLock(f *os.File) (busy bool, err error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return false, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return true, nil
		}
		return false, err
	}
}

func unlockFile(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
