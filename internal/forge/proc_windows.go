//go:build windows

package forge

import (
	"os/exec"

	"ramus/internal/git/gitexe"
)

// runTool runs cmd with no window, in a job of its own (ownGroup), which
// it is in before it runs anything.
func runTool(cmd *exec.Cmd) error {
	j := ownGroup(cmd)
	j.Suspend(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// A job that cannot be had leaves cancelling to end the tool's
	// process tree one by one.
	j.Join(cmd)
	return cmd.Wait()
}

// ownGroup gives cmd no console window: the app is a program of the
// desktop, with no console, and Windows gives a console program started
// from one a window of its own, which would flash up and take the focus
// at every call. What the tool starts shares its console, as hidden.
// Cancelling ends the job the tool runs in, with what it started (git,
// under gh stack), which would otherwise hold the pipes read until
// WaitDelay.
func ownGroup(cmd *exec.Cmd) *gitexe.Job { return gitexe.InJob(cmd, false) }
