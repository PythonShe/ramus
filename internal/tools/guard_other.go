//go:build !windows

package tools

import "os/exec"

// guard has nothing to do: arguments reach the program as they are.
func guard(cmd *exec.Cmd) error { return nil }
