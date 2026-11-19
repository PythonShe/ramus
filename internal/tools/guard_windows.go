package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// guard has a batch file run through cmd.exe with a command line quoted
// for it. Windows would run it through cmd.exe all the same, with the
// command line Go quotes for other programs: a file named x&calc.md, or
// a branch named so, would then run calc.
func guard(cmd *exec.Cmd) error {
	if !isBatch(cmd.Path) {
		return nil
	}
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	argv := append([]string{cmd.Path}, cmd.Args[1:]...)
	line, err := batchLine(shell, argv)
	if err != nil {
		return err
	}
	cmd.Path = shell
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = line
	return nil
}
