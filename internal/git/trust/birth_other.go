//go:build !windows && !linux && !darwin

package trust

// birth is "" on the systems the app is not built for: the device and
// inode alone tell folders apart there.
func birth(string) string { return "" }
