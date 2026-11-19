//go:build windows

package run

import (
	"os/exec"
	"sync"

	"ramus/internal/git/gitexe"
)

// stopsHard: cancelling ends git with TerminateJobObject, which leaves it
// no chance to remove its lock files, as it does on SIGTERM elsewhere.
const stopsHard = true

// lower leaves the priority as it is: Windows has no nice.
func lower(cmd *exec.Cmd) {}

// groups holds the job (gitexe.Job) of each command prepared and not
// started yet.
var groups sync.Map // *exec.Cmd -> *gitexe.Job

// stopGroup has cmd start hidden, in a job of its own that cancelling
// ends, with everything git starts; dies ends the job with the app.
func stopGroup(cmd *exec.Cmd, dies bool) {
	groups.Store(cmd, gitexe.InJob(cmd, dies))
}

// joinGroup puts a process just started in its job, and lets it run if
// it was created suspended. A job that cannot be had leaves cancelling
// to end git's process tree one by one.
func joinGroup(cmd *exec.Cmd) {
	if v, ok := groups.LoadAndDelete(cmd); ok {
		v.(*gitexe.Job).Join(cmd)
	}
}

// watchGroup has cmd, one Run starts and waits on, created suspended, for
// joinGroup to let run once it is in its job: os/exec cannot put a process
// in a job as it is created. Command's processes are not, as a caller
// that started one without Runner.Start would leave it suspended for good.
// Nothing is left to do once Wait has returned: ending the job ends all of
// it at once.
func watchGroup(cmd *exec.Cmd) (waited func()) {
	if v, ok := groups.Load(cmd); ok {
		v.(*gitexe.Job).Suspend(cmd)
	}
	return func() {}
}

// leaveGroup forgets the group of a command that did not start.
func leaveGroup(cmd *exec.Cmd) { groups.Delete(cmd) }
