//go:build !darwin

package main

// peakRSS is measured on macOS only; the spike ran there.
func peakRSS() int64 { return 0 }
