//go:build !windows

package statefile

import "os"

// rename replaces to with from, while ok, when given, says it may.
func rename(from, to string, ok func() bool) error {
	if ok != nil && !ok() {
		return ErrChanged
	}
	return os.Rename(from, to)
}

// syncDir has the rename itself reach the disk. A folder that cannot be
// synced leaves the file as durable as the system makes it anyway.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
