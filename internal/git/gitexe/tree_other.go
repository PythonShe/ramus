//go:build !unix

package gitexe

import "os/exec"

// stopTree leaves cancelling to stop git alone; WaitDelay still bounds
// the wait for what it started.
func stopTree(cmd *exec.Cmd) {}
