// Package git is the engine: it reads repositories and runs git for
// everything that writes.
//
// It imports nothing from mygo, so that it can be tested, benchmarked and
// reused without a window; scripts/check.sh enforces this.
package git
