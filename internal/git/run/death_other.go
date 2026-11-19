//go:build unix && !linux

package run

import "syscall"

// dieWithApp has nothing to set: only Linux signals a process when the
// one that started it ends. A read ends when it next writes to the app,
// gone; Runner.Quit stops the rest when the app quits.
func dieWithApp(a *syscall.SysProcAttr) {}
