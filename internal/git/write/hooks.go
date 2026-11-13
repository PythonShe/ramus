package write

import (
	"bytes"
	"context"
	"errors"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/run"
)

// Hook is one command git runs for a hook event.
type Hook struct {
	// Name is the hook's name in config, or "hook from hookdir" for the
	// script in the hooks directory, as git lists them.
	Name string
	// Scope is the config scope that defined it ("local", "global"),
	// empty for the hooks directory.
	Scope string
	// Disabled is a hook turned off with hook.<name>.enabled=false: listed,
	// but git does not run it.
	Disabled bool
}

// ErrNoHookList means this git cannot list hooks (before 2.54). The app
// does not scan the hooks directory instead: that would miss hooks
// defined in config and show hooks git would not run.
var ErrNoHookList = errors.New("write: listing hooks needs git 2.54 or newer")

// Hooks lists what git runs for event, through git hook list, so hooks
// from config are seen and the hooks directory is git's to resolve.
func Hooks(ctx context.Context, r *run.Runner, gitDir, workTree, event string) ([]Hook, error) {
	if !r.Git().Version.Has(gitexe.HookList) {
		return nil, ErrNoHookList
	}
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true, Untranslated: true,
		Args: []string{"hook", "list", "-z", "--show-scope", event}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 && bytes.Contains([]byte(e.Stderr), []byte("no hooks found")) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hooks []Hook
	for _, rec := range bytes.Split(res.Stdout, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		scope, name, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			hooks = append(hooks, Hook{Name: string(rec)})
			continue
		}
		h := Hook{Name: string(name), Scope: string(scope)}
		if rest, ok := bytes.CutPrefix(name, []byte("disabled\t")); ok {
			h.Name, h.Disabled = string(rest), true
		}
		hooks = append(hooks, h)
	}
	return hooks, nil
}
