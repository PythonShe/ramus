//go:build windows

package run

import (
	"os/exec"
	"sync"

	"golang.org/x/sys/windows"
)

// lower leaves the priority as it is: Windows has no nice.
func lower(cmd *exec.Cmd) {}

// group is the job object a process and everything it starts run in,
// which Windows passes on to each child as it is made: cancelling ends
// all of it, as a process group does elsewhere. Killing git alone would
// leave a hook's shell and what that runs going, holding the pipes the
// app reads until WaitDelay, and the folders they run in.
type group struct {
	mu        sync.Mutex
	job       windows.Handle
	cancelled bool
}

// groups holds the group of each command prepared and not started yet.
var groups sync.Map // *exec.Cmd -> *group

func stopGroup(cmd *exec.Cmd) {
	g := &group{}
	groups.Store(cmd, g)
	cmd.Cancel = func() error {
		g.mu.Lock()
		g.cancelled = true
		if g.job != 0 {
			windows.TerminateJobObject(g.job, 1)
		}
		g.mu.Unlock()
		return cmd.Process.Kill()
	}
}

// joinGroup puts a process just started in its job. It runs before git
// can start anything: what it starts is in the job from the first.
func joinGroup(cmd *exec.Cmd) {
	v, ok := groups.LoadAndDelete(cmd)
	if !ok {
		return
	}
	g := v.(*group)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(h)
		windows.CloseHandle(job)
		return
	}
	g.mu.Lock()
	g.job = job
	if g.cancelled {
		windows.TerminateJobObject(job, 1)
	}
	g.mu.Unlock()
	// Once git has exited, as on other systems, what it left running is
	// let be: the job is let go without ending it.
	go func() {
		windows.WaitForSingleObject(h, windows.INFINITE)
		windows.CloseHandle(h)
		g.mu.Lock()
		windows.CloseHandle(g.job)
		g.job = 0
		g.mu.Unlock()
	}()
}

// leaveGroup forgets the group of a command that did not start.
func leaveGroup(cmd *exec.Cmd) { groups.Delete(cmd) }
