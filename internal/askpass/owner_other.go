//go:build !unix

package askpass

import "io/fs"

// ownedByMe is true: the temporary folder is the user's own.
func ownedByMe(info fs.FileInfo) bool { return true }
