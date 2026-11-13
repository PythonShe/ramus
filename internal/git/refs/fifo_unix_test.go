//go:build unix

package refs

import "syscall"

func syscallMkfifo(path string) error { return syscall.Mkfifo(path, 0o644) }
