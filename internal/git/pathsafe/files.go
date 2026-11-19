package pathsafe

import (
	"os"

	"ramus/internal/git/osfile"
)

// The engine opens files through osfile, which reads only a regular
// file, to a bound, and lets git replace it meanwhile. These are its
// names here, kept for callers outside the engine's own readers.
var (
	// ErrNotRegular is osfile.ErrNotRegular.
	ErrNotRegular = osfile.ErrNotRegular
	// ErrTooLarge is osfile.ErrTooLarge.
	ErrTooLarge = osfile.ErrTooLarge
)

// OpenRegular is osfile.Open.
func OpenRegular(path string) (*os.File, error) { return osfile.Open(path) }

// ReadFile is osfile.ReadFile.
func ReadFile(path string, limit int64) ([]byte, error) { return osfile.ReadFile(path, limit) }
