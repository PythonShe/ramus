//go:build !unix

package gitexe

import "os/exec"

// StopTree leaves cancelling to stop git alone; WaitDelay still bounds
// the wait for what it started.
func StopTree(cmd *exec.Cmd) {}
