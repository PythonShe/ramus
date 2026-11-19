//go:build !unix && !windows

package actions

import "os/exec"

func inGroup(cmd *exec.Cmd)               {}
func joined(cmd *exec.Cmd)                {}
func ended(cmd *exec.Cmd, cancelled bool) {}
