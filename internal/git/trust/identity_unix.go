//go:build !windows

package trust

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// identity names the folder at path apart from any other the path may
// name later: its device and inode, and its birth time where the system
// tells it (birth).
//
// The device number is the volume as the system mounted it this time:
// an external drive or a network share mounted again can have another,
// and its repositories then ask to be trusted again. That is the safe
// side, and kept: a number that survives a remount would be one the
// volume itself carries (its UUID, which statfs's f_fsid does not give
// stably either), and a volume made to stand at the same mount point
// can carry a copied one, where the device number is the system's.
// Windows names the volume by the serial number its format wrote
// (identity_windows.go), which survives a remount, as the file index
// does.
func identity(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("trust: no file identity here")
	}
	id := fmt.Sprintf("%d:%d", uint64(st.Dev), uint64(st.Ino))
	if b := birth(path); b != "" {
		id += ":" + b
	}
	return id, nil
}
