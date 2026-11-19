//go:build !windows

package pathsafe

import "syscall"

const openNonblock = syscall.O_NONBLOCK
