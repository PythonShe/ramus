package trust

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// birth is when the folder at path was made, "" where the file system
// does not keep it. Linux file systems give an inode number freed to the
// next file made, so a folder made again at a trusted path (an archive
// extracted again) can have the inode of the one trusted; it was not
// made at the same moment.
func birth(path string) string {
	var st unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME, &st) != nil || st.Mask&unix.STATX_BTIME == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%09d", st.Btime.Sec, st.Btime.Nsec)
}
