//go:build windows

package repo

import "os"

// fileID is 0 on Windows: the file index needs an open handle, and size
// and a 100 ns modification time tell a rewrite apart well enough there.
func fileID(os.FileInfo) uint64 { return 0 }
