//go:build darwin

package pathsafe

import (
	"bytes"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// pathMax is a variable so that the buffer made of it is on the heap,
// where nothing moves it while the system writes to its address.
var pathMax = unix.PathMax

// spelled asks the system for the path of what real names (F_GETPATH),
// which it gives in the case and Unicode form the disk holds; real as it
// is when it cannot. The path has its symlinks resolved already.
func spelled(real string) string {
	// For events only: needs no read permission, and does not wait on a
	// named pipe or keep a volume from unmounting.
	fd, err := unix.Open(real, unix.O_EVTONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return real
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Nlink > 1 {
		// A file with several names may be given by another.
		return real
	}
	buf := make([]byte, pathMax)
	_, err = unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buf[0]))))
	runtime.KeepAlive(buf)
	if err != nil {
		return real
	}
	n := bytes.IndexByte(buf, 0)
	if n <= 0 {
		return real
	}
	return string(buf[:n])
}
