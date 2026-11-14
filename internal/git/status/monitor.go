package status

import (
	"strings"
	"time"

	"ramus/internal/git/config"
	"ramus/internal/git/gitexe"
	"ramus/internal/git/run"
)

// SlowStatus is how long status runs before turning on git's file system
// monitor is worth offering.
const SlowStatus = 500 * time.Millisecond

// MonitorAdvice is an offer to turn on git's built-in file system monitor
// and the untracked cache, which make status in a large work tree read
// only what changed. The app shows the exact commands and runs them only
// when the user says so.
type MonitorAdvice struct {
	Reason string
	// Cmds set the config, in order.
	Cmds []run.Cmd
	// Daemon says what keeps running and how to stop it, when the
	// monitor is among what is turned on.
	Daemon string
}

// AdviseMonitor says whether to offer the monitor and the untracked cache
// for a work tree whose status took took. It offers them only in a
// trusted repository, only git's own daemon (a core.fsmonitor naming a
// hook or set to anything at all is the user's choice), on Linux only from
// the git that has the daemon there, and only what is not set yet.
func AdviseMonitor(goos string, v gitexe.Version, cfg *config.Config, gitDir, workTree string, trusted bool, took time.Duration) (MonitorAdvice, bool) {
	if !trusted || workTree == "" || took < SlowStatus || cfg == nil {
		return MonitorAdvice{}, false
	}
	daemon := goos == "darwin" || goos == "windows" || goos == "linux" && v.Has(gitexe.FSMonitorLinux)
	var a MonitorAdvice
	var what []string
	set := func(key string) {
		a.Cmds = append(a.Cmds, run.Cmd{GitDir: gitDir, WorkTree: workTree, Args: []string{"config", key, "true"}})
	}
	if _, ok := cfg.Get("core.fsmonitor"); !ok && daemon {
		set("core.fsmonitor")
		what = append(what, "git's file system monitor")
		a.Daemon = "git starts a daemon that watches this work tree; it keeps running after Ramus closes. " +
			"git fsmonitor--daemon stop stops it, and git config --unset core.fsmonitor turns it off."
	}
	if _, ok := cfg.Get("core.untrackedCache"); !ok {
		set("core.untrackedCache")
		what = append(what, "the untracked cache")
	}
	if len(a.Cmds) == 0 {
		return MonitorAdvice{}, false
	}
	a.Reason = "Status took " + took.Round(10*time.Millisecond).String() + ". " +
		strings.ToUpper(what[0][:1]) + what[0][1:]
	if len(what) > 1 {
		a.Reason += " and " + what[1]
	}
	a.Reason += " would make it read only what changed."
	return a, true
}
