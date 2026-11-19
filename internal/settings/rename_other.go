//go:build !windows

package settings

import "os"

func rename(from, to string) error { return os.Rename(from, to) }

// syncDir has the rename itself reach the disk. A folder that cannot be
// synced leaves the file as durable as the system makes it anyway.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
