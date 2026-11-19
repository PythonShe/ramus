//go:build !unix && !windows

package forge

import "os/exec"

// runTool runs cmd, left to exec's default kill.
func runTool(cmd *exec.Cmd) error { return cmd.Run() }
