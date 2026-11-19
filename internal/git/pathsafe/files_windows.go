//go:build windows

package pathsafe

// Windows has no named pipes in the file system's own namespace.
const openNonblock = 0
