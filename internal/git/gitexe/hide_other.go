//go:build !windows

package gitexe

import "os/exec"

// Hide has nothing to do: only Windows opens a window for a console
// program.
func Hide(cmd *exec.Cmd) {}
