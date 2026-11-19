//go:build !windows && !linux

package trust

// birth is "" here: macOS's file systems (APFS, HFS+) number files in
// increasing order and do not give a freed number again, so the inode
// alone tells folders apart.
func birth(string) string { return "" }
