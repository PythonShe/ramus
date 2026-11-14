//go:build !windows

package repo

import (
	"os"
	"syscall"
)

// FileID is the identity of the file fi describes: its inode.
func FileID(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
