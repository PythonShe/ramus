//go:build windows

package gitexe

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job is the job object a process and everything it starts run in, which
// Windows passes on to each child as it is made: cancelling ends all of
// it, as a process group does elsewhere. Killing the process alone would
// leave what it started (a hook's shell, git under gh) holding the pipes
// read until WaitDelay, and the folders they run in. git, the forge tools
// and the user's actions all run this way.
type Job struct {
	mu        sync.Mutex
	h         windows.Handle
	cancelled bool
	// suspended is a process created suspended, so that it is in its job
	// before it runs (see Suspend).
	suspended bool
	// dies ends the job with the app, should it end while the process
	// runs.
	dies bool
	// tree is set when the job could not be made or joined: cancelling
	// then ends the process's descendants one by one.
	tree bool
	pid  uint32
}

// InJob readies cmd to start with no console window (Hide), in a job of
// its own that cancelling cmd ends, and returns the job, for Join once
// cmd has started. dies has the job, and all in it, end with the app
// while the process runs.
func InJob(cmd *exec.Cmd, dies bool) *Job {
	Hide(cmd)
	j := &Job{dies: dies}
	cmd.Cancel = func() error {
		j.mu.Lock()
		j.cancelled = true
		switch {
		case j.h != 0:
			windows.TerminateJobObject(j.h, 1)
		case j.tree:
			killTree(j.pid)
		}
		j.mu.Unlock()
		return cmd.Process.Kill()
	}
	return j
}

// Suspend has cmd, not started yet, created suspended, so that it is in
// its job before it runs anything: one that is not may start a child, as
// the git.exe of Git\cmd starts the real git at once, that escapes the
// job in the moment before Join. Only for a process Join is sure to
// follow the start of: it would otherwise stay suspended for good.
func (j *Job) Suspend(cmd *exec.Cmd) {
	j.suspended = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

// Dies reports whether the job ends with the app.
func (j *Job) Dies() bool { return j.dies }

// Join puts the process cmd just started in the job, and lets it run if
// it was created suspended. When the job cannot be made or joined, the
// process runs without one, cancelling ends it and its descendants one
// by one, and the error says why. Once the process has exited, what it
// left running is let be, as on other systems: the job is let go
// without ending it. A command cancelled as it started has no error:
// the cancel, killing the process or ending its job beside Join, makes
// Join fail, which says nothing of whether jobs can be had.
func (j *Job) Join(cmd *exec.Cmd) (err error) {
	// Deferred first, to run last: after a failure to resume.
	defer func() {
		j.mu.Lock()
		if j.cancelled {
			err = nil
		}
		j.mu.Unlock()
	}()
	pid := uint32(cmd.Process.Pid)
	if j.suspended {
		defer func() {
			if rerr := resume(pid); rerr != nil {
				// Never left suspended: Wait would wait for good.
				cmd.Process.Kill()
				err = errors.Join(err, rerr)
			}
		}()
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		j.fallBack(pid)
		return fmt.Errorf("gitexe: process %d: %w", pid, err)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		windows.CloseHandle(h)
		j.fallBack(pid)
		return fmt.Errorf("gitexe: job for process %d: %w", pid, err)
	}
	if j.dies {
		// The app holds the job's one handle: when it ends, crashed or
		// killed, the handle closes and the job ends with it.
		killOnClose(job, true)
	}
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		windows.CloseHandle(h)
		j.fallBack(pid)
		return fmt.Errorf("gitexe: process %d into its job: %w", pid, err)
	}
	j.mu.Lock()
	j.h = job
	if j.cancelled {
		windows.TerminateJobObject(job, 1)
	}
	j.mu.Unlock()
	go func() {
		windows.WaitForSingleObject(h, windows.INFINITE)
		windows.CloseHandle(h)
		j.mu.Lock()
		if j.dies {
			killOnClose(j.h, false)
		}
		windows.CloseHandle(j.h)
		j.h = 0
		j.mu.Unlock()
	}()
	return nil
}

// fallBack has cancelling end the process tree of pid, for a process the
// job could not take.
func (j *Job) fallBack(pid uint32) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.tree, j.pid = true, pid
	if j.cancelled {
		killTree(pid)
	}
}

// killOnClose has job end, or no longer end, when its last handle closes.
func killOnClose(job windows.Handle, on bool) {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if on {
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
}

// resume lets every thread of process pid run: the one thread of a
// process created suspended.
func resume(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	found := false
	for e := windows.Thread32First(snap, &te); e == nil; e = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(th)
		windows.CloseHandle(th)
		if err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("gitexe: no thread to resume")
	}
	return nil
}

// killTree ends the descendants of process pid, deepest first; the
// caller ends pid itself. A process keeps the id of its parent after
// that parent exits, and Windows gives the id to another process later:
// one created before the process its parent id now names is not that
// process's child, and is let be. So is one created after the snapshot,
// which was not in it: the child it names exited and its id went to
// another before it was opened.
func killTree(pid uint32) {
	// Read before the snapshot, on the system clock that creation times
	// are on: a child made just before the snapshot may be let be, but
	// no process made after the snapshot is ended.
	var ft windows.Filetime
	windows.GetSystemTimeAsFileTime(&ft)
	taken := ft.Nanoseconds()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	children := map[uint32][]uint32{}
	pe := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for e := windows.Process32First(snap, &pe); e == nil; e = windows.Process32Next(snap, &pe) {
		if pe.ProcessID != pe.ParentProcessID {
			children[pe.ParentProcessID] = append(children[pe.ParentProcessID], pe.ProcessID)
		}
	}
	root, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return
	}
	since, ok := created(root)
	windows.CloseHandle(root)
	if !ok {
		return
	}
	var end func(p uint32, since int64, depth int)
	end = func(p uint32, since int64, depth int) {
		if depth > 64 {
			return // a cycle of reused ids
		}
		for _, c := range children[p] {
			// Held open across its own descendants, so that its id is not
			// given to another before it is ended.
			h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, c)
			if err != nil {
				continue
			}
			if t, ok := created(h); ok && t >= since && t <= taken {
				end(c, t, depth+1)
				windows.TerminateProcess(h, 1)
			}
			windows.CloseHandle(h)
		}
	}
	end(pid, since, 0)
}

// created returns when the process was created.
func created(h windows.Handle) (int64, bool) {
	var c, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &c, &exit, &kernel, &user) != nil {
		return 0, false
	}
	return c.Nanoseconds(), true
}

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// Holds reports whether the process with handle process, which has
// PROCESS_QUERY_LIMITED_INFORMATION, is in this job, the one Join made,
// and not only in some job: a child is in every job of its parent's. It
// is false once the job's process has exited and the job is let go.
func (j *Job) Holds(process windows.Handle) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.h == 0 {
		return false, nil
	}
	var in int32
	if ok, _, err := isProcessInJob.Call(uintptr(process), uintptr(j.h), uintptr(unsafe.Pointer(&in))); ok == 0 {
		return false, err
	}
	return in != 0, nil
}
