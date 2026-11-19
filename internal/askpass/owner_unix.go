//go:build unix

package askpass

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByMe reports a file of the user the app runs as: the temporary
// folder may be shared with other users.
func ownedByMe(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
