package run

import (
	"context"
	"syscall"
	"testing"
)

// A read, or a command no one waits on, is told to end when the app ends;
// a write the user started is not.
func TestDiesWithApp(t *testing.T) {
	r := runner(t, 1)
	for _, c := range []struct {
		cmd  *Cmd
		want syscall.Signal
	}{
		{&Cmd{GitDir: t.TempDir(), Read: true, Args: []string{"version"}}, syscall.SIGTERM},
		{&Cmd{GitDir: t.TempDir(), NoPrompt: true, Args: []string{"fetch"}}, syscall.SIGTERM},
		{&Cmd{GitDir: t.TempDir(), Args: []string{"commit"}}, 0},
	} {
		if got := r.command(context.Background(), c.cmd).SysProcAttr.Pdeathsig; got != c.want {
			t.Errorf("%v: Pdeathsig %v, want %v", c.cmd.Args, got, c.want)
		}
	}
}
