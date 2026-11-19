package run

import "syscall"

// dieWithApp has git sent SIGTERM when the app ends, crashed or killed;
// what git started ends with it as their pipes close.
func dieWithApp(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGTERM }
