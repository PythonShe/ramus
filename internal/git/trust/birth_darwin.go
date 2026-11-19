package trust

import (
	"fmt"
	"os"
	"syscall"
)

// birth is when the folder at path was made. APFS does not give a freed
// inode number again, but HFS+ does once its numbers wrap, and the birth
// time costs nothing more: os.Stat reads it with the inode.
func birth(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Birthtimespec.Sec == 0 && st.Birthtimespec.Nsec == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%09d", st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
}
