package actions

import (
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// jobs holds the job object of each action running, which Windows passes
// on to every process it starts: ending the job ends all of them, as a
// process group does elsewhere.
var jobs sync.Map // *exec.Cmd -> *job

type job struct {
	mu        sync.Mutex
	h         windows.Handle
	cancelled bool
}

// inGroup readies cmd to run in a job of its own, and without a console
// window: the app has none, so Windows would open a new, visible one for
// each console program an action starts, taking the focus.
func inGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	j := &job{}
	jobs.Store(cmd, j)
	cmd.Cancel = func() error {
		j.mu.Lock()
		j.cancelled = true
		if j.h != 0 {
			windows.TerminateJobObject(j.h, 1)
		}
		j.mu.Unlock()
		return cmd.Process.Kill()
	}
}

// joined puts the process just started in its job, before it has had
// the time to start anything.
func joined(cmd *exec.Cmd) {
	v, ok := jobs.Load(cmd)
	if !ok {
		return
	}
	j := v.(*job)
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(h)
		return
	}
	defer windows.CloseHandle(p)
	if err := windows.AssignProcessToJobObject(h, p); err != nil {
		windows.CloseHandle(h)
		return
	}
	j.mu.Lock()
	j.h = h
	if j.cancelled {
		windows.TerminateJobObject(h, 1)
	}
	j.mu.Unlock()
}

// ended ends what is left of the job of an action cancelled, and lets the
// job go.
func ended(cmd *exec.Cmd, cancelled bool) {
	v, ok := jobs.LoadAndDelete(cmd)
	if !ok {
		return
	}
	j := v.(*job)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.h == 0 {
		return
	}
	if cancelled {
		windows.TerminateJobObject(j.h, 1)
	}
	windows.CloseHandle(j.h)
	j.h = 0
}
