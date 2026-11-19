//go:build !windows

package trust

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// identity names the folder at path apart from any other the path may
// name later: its device and inode.
func identity(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("trust: no file identity here")
	}
	return fmt.Sprintf("%d:%d", uint64(st.Dev), uint64(st.Ino)), nil
}
