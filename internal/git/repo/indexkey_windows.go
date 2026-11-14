//go:build windows

package repo

import "os"

// FileID is 0 on Windows: the file index needs an open handle, and size
// and a 100 ns modification time tell a rewrite apart well enough there.
func FileID(os.FileInfo) uint64 { return 0 }
