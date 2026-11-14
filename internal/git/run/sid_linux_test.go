package run

import "syscall"

// getsid is getsid(2), which Linux's syscall package leaves out.
func getsid(pid int) (int, error) {
	sid, _, errno := syscall.RawSyscall(syscall.SYS_GETSID, uintptr(pid), 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(sid), nil
}
