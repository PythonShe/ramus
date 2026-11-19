package actions

import (
	"os/exec"
	"sync"

	"ramus/internal/git/gitexe"
)

// jobs holds the job object of each action running, which Windows passes
// on to every process it starts: ending the job ends all of them, as a
// process group does elsewhere.
var jobs sync.Map // *exec.Cmd -> *gitexe.Job

// inGroup readies cmd to run in a job of its own, which it is in before
// it runs anything, and without a console window: the app has none, so
// Windows would open a new, visible one for each console program an
// action starts, taking the focus. Cancelling ends the job.
func inGroup(cmd *exec.Cmd) {
	j := gitexe.InJob(cmd, false)
	j.Suspend(cmd)
	jobs.Store(cmd, j)
}

// joined puts the process just started in its job and lets it run. A job
// that cannot be had leaves cancelling to end the action's process tree
// one by one.
func joined(cmd *exec.Cmd) {
	if v, ok := jobs.Load(cmd); ok {
		v.(*gitexe.Job).Join(cmd)
	}
}

// ended forgets the job of an action that ended or did not start: one
// cancelled was ended whole as it was cancelled, and the job lets go of
// itself once the action's process has exited.
func ended(cmd *exec.Cmd, cancelled bool) { jobs.Delete(cmd) }
