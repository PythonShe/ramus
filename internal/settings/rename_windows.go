package settings

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// renameTries bounds how long a rename waits for a file held open
// elsewhere: an editor showing the settings, a virus scanner or the
// search indexer reading a file just written.
const renameTries = 8

// rename replaces to with from. Windows refuses to replace a file another
// program has open without sharing its deletion, which those programs
// hold only for a moment, so the rename is tried again a few times; ok,
// when given, says before each try whether to go on: the program may have
// written the file meanwhile.
func rename(from, to string, ok func() bool) error {
	wait := 10 * time.Millisecond
	for i := 0; ; i++ {
		if ok != nil && !ok() {
			return errChanged
		}
		err := os.Rename(from, to)
		if err == nil || i == renameTries-1 || !busy(err) {
			return err
		}
		time.Sleep(wait)
		wait *= 2
	}
}

func busy(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// syncDir has nothing to do: Windows has no handle on a folder to flush,
// and NTFS journals the rename.
func syncDir(dir string) {}
