//go:build windows

package run

import "os/exec"

// lower leaves the priority as it is: Windows has no nice.
func lower(cmd *exec.Cmd) {}

// stopGroup leaves Windows processes to exec's default kill; git for
// Windows ends its children when its console goes.
func stopGroup(cmd *exec.Cmd) {}
