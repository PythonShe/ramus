package watch

import (
	"errors"
	"syscall"
)

// What ReadDirectoryChangesW ends a read with that is not the end of the
// watch; Windows' values, kept here so the choice is tested everywhere.
const (
	errInvalidParameter syscall.Errno = 87   // ERROR_INVALID_PARAMETER
	errNotifyEnumDir    syscall.Errno = 1022 // ERROR_NOTIFY_ENUM_DIR
)

// shareBufferSize is the most ReadDirectoryChangesW takes for a folder on
// a network share.
const shareBufferSize = 64 << 10

type afterRead int

const (
	readOn      afterRead = iota
	readSmaller           // again, with a buffer of shareBufferSize
	readDropped           // notices were lost; the watch goes on
	readFailed            // the watch stops
)

// windowsReadEnded says what the Windows watcher does after a read that
// ended with err, the first that would have returned changes when first,
// into a buffer of size bytes.
func windowsReadEnded(err error, first bool, size int) afterRead {
	switch {
	case err == nil:
		return readOn
	case first && errors.Is(err, errInvalidParameter) && size > shareBufferSize:
		// A network share takes no larger buffer.
		return readSmaller
	case errors.Is(err, errNotifyEnumDir):
		// Too many changes for the buffer, said as an error.
		return readDropped
	}
	return readFailed
}
