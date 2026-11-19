//go:build !windows

package write

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// openHolders finds who has path open without lsof, through /proc where
// the system has it, as Linux does: each process's open files are links
// in /proc/<pid>/fd. Those of another user's processes cannot be read,
// as lsof cannot read them unless run as root.
func openHolders(ctx context.Context, path string) ([]Holder, error) {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, errNoLsof
	}
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		return nil, errNoLsof
	}
	// The kernel names an open file by its path with links resolved.
	want := path
	if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		want = filepath.Join(dir, filepath.Base(path))
	}
	var hs []Holder
	for _, p := range procs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		fd := filepath.Join("/proc", p.Name(), "fd")
		fds, err := os.ReadDir(fd)
		if err != nil {
			continue // gone, or another user's
		}
		for _, f := range fds {
			if target, err := os.Readlink(filepath.Join(fd, f.Name())); err == nil && (target == want || target == path) {
				comm, _ := os.ReadFile(filepath.Join("/proc", p.Name(), "comm"))
				hs = append(hs, Holder{PID: pid, Command: strings.TrimSpace(string(comm))})
				break
			}
		}
	}
	return hs, nil
}
