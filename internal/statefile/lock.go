package statefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked is a state file another writer held locked past the wait.
var ErrLocked = errors.New("another copy of the app is writing it")

// lockPoll is how often Lock tries again while the lock is held.
const lockPoll = 5 * time.Millisecond

// Lock takes the lock of the state file at path, for a writer that reads
// the file, merges and replaces it: two such writers at once, two copies
// of the app, would each replace the file with what it read before the
// other's rename, and one's change would be lost. It waits up to wait
// for a writer holding it, then fails with ErrLocked; unlock releases it.
//
// The lock is a file of its own beside path, as named, never through a
// link: ".<name>.lock", left in place, as removing it would let a third
// writer lock another file. It is the system's advisory lock, released
// when the process ends however it ends. A file system that has no such
// locks writes without one. Writers that do not take it (an editor) are
// not kept out: WriteFileIf's check before the rename is what guards
// against them.
func Lock(path string, wait time.Duration) (unlock func(), err error) {
	name := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock")
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		busy, err := tryLock(f)
		switch {
		case err != nil:
			// No locks here: as without them.
			f.Close()
			return func() {}, nil
		case !busy:
			return func() {
				unlockFile(f)
				f.Close()
			}, nil
		case time.Now().After(deadline):
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, ErrLocked)
		}
		time.Sleep(lockPoll)
	}
}
