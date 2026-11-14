//go:build unix

package main

import (
	"runtime"
	"syscall"
	"time"
)

// peakMemory is the most this process has held resident, and the most
// one of its children waited for held; bytes, 0 when unknown.
func peakMemory() (self, children int64) {
	var s, c syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &s)
	syscall.Getrusage(syscall.RUSAGE_CHILDREN, &c)
	// macOS reports bytes, Linux kibibytes.
	unit := int64(1)
	if runtime.GOOS != "darwin" {
		unit = 1024
	}
	if runtime.GOOS == "linux" {
		// Go starts children with CLONE_VM, and exec records the shared
		// memory's peak as the child's: Linux reports the app's peak for
		// git. 0 says unknown.
		return int64(s.Maxrss) * unit, 0
	}
	return int64(s.Maxrss) * unit, int64(c.Maxrss) * unit
}

// cpuTime is the CPU time this process and its children waited for have
// used.
func cpuTime() time.Duration {
	var s, c syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &s)
	syscall.Getrusage(syscall.RUSAGE_CHILDREN, &c)
	tv := func(t syscall.Timeval) time.Duration { return time.Duration(t.Nano()) }
	return tv(s.Utime) + tv(s.Stime) + tv(c.Utime) + tv(c.Stime)
}
