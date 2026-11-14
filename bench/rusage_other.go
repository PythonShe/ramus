//go:build !unix

package main

import "time"

// peakMemory is not read here; 0 says unknown.
func peakMemory() (self, children int64) { return 0, 0 }

// cpuTime is not read here.
func cpuTime() time.Duration { return 0 }
