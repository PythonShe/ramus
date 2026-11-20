//go:build !windows

package osfile

import "os"

// renameOver renames from over to as git does, which here is a plain
// rename. On Windows git renames with POSIX semantics, which os.Rename
// does not: it refuses while to is open, however it was opened.
func renameOver(from, to string) error { return os.Rename(from, to) }
