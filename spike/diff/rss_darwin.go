package main

import "syscall"

// peakRSS returns the process's peak resident memory in bytes.
func peakRSS() int64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return ru.Maxrss // bytes on macOS
}
