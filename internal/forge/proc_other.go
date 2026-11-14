//go:build !unix

package forge

import "os/exec"

// ownGroup leaves the process to exec's default kill.
func ownGroup(cmd *exec.Cmd) {}
