package watch

import (
	"fmt"
	"syscall"
	"testing"
)

// The Windows watcher retries its first read with a buffer a network
// share takes, and goes on after an overflow said as an error; other
// errors stop it.
func TestWindowsReadEnded(t *testing.T) {
	for _, c := range []struct {
		err   error
		first bool
		size  int
		want  afterRead
	}{
		{nil, true, 1 << 20, readOn},
		{errInvalidParameter, true, 1 << 20, readSmaller},
		{fmt.Errorf("read: %w", errInvalidParameter), true, 1 << 20, readSmaller},
		{errInvalidParameter, true, shareBufferSize, readFailed},
		{errInvalidParameter, false, 1 << 20, readFailed},
		{errNotifyEnumDir, false, 1 << 20, readDropped},
		{errNotifyEnumDir, true, shareBufferSize, readDropped},
		{syscall.Errno(5), true, 1 << 20, readFailed},
	} {
		if got := windowsReadEnded(c.err, c.first, c.size); got != c.want {
			t.Errorf("%v, first %v, %d bytes: %v, want %v", c.err, c.first, c.size, got, c.want)
		}
	}
}
