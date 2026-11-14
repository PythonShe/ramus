// Package spawn measures what starting git costs, the work Windows makes
// dearest: one process through the runner, and opening a repository to
// its first status, which starts a dozen. Run it on the system measured:
//
//	RAMUS_MEASURE=1 go test ./spike/spawn -run Spawn -v
package spawn
