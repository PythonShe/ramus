//go:build unix && !linux

package run

import "syscall"

func getsid(pid int) (int, error) { return syscall.Getsid(pid) }
