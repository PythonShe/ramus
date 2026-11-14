package repo

import (
	"os"
	"path/filepath"
)

// IndexKey says whether the index changed: its size, modification time
// to the nanosecond and file identity. git replaces the index by renaming
// a new file over it, so a write changes the identity even within one
// timestamp tick. The checksum is not read: that would read the whole
// file (gitui #2823).
type IndexKey struct {
	Size, MTime int64
	ID          uint64
}

// ReadIndexKey returns the key of the index in gitDir; the zero key, with
// no error, when there is no index yet.
func ReadIndexKey(gitDir string) (IndexKey, error) {
	fi, err := os.Stat(filepath.Join(gitDir, "index"))
	if os.IsNotExist(err) {
		return IndexKey{}, nil
	}
	if err != nil {
		return IndexKey{}, err
	}
	return IndexKey{Size: fi.Size(), MTime: fi.ModTime().UnixNano(), ID: FileID(fi)}, nil
}
