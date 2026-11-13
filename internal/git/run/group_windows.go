//go:build windows

package run

import "os/exec"

// stopGroup leaves Windows processes to exec's default kill; git for
// Windows ends its children when its console goes.
func stopGroup(cmd *exec.Cmd) {}
