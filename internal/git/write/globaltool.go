package write

import (
	"context"
	"errors"
	"os"
	"strings"

	"ramus/internal/git/run"
)

// GlobalTool is a program the user's global git config may name as its
// difftool and mergetool: Name in diff.tool and merge.tool, and the
// commands git runs in difftool.<Name>.cmd and mergetool.<Name>.cmd.
type GlobalTool struct {
	Name              string
	DiffCmd, MergeCmd string
}

// ToolIsSet reports whether the user's global git config makes tool its
// difftool and mergetool.
func ToolIsSet(ctx context.Context, r *run.Runner, tool GlobalTool) (bool, error) {
	for _, key := range []string{"diff.tool", "merge.tool"} {
		v, err := globalGet(ctx, r, key)
		if err != nil || v != tool.Name {
			return false, err
		}
	}
	return true, nil
}

// SetTool makes tool the difftool and mergetool of the user's global git
// config, or, with on false, takes it out: the tool's own sections go,
// and diff.tool and merge.tool only while they still name it, so a tool
// the user chose since stays.
func SetTool(ctx context.Context, r *run.Runner, tool GlobalTool, on bool) error {
	if err := checkName("tool", tool.Name); err != nil {
		return err
	}
	if on {
		for _, kv := range [][2]string{
			{"diff.tool", tool.Name},
			{"difftool." + tool.Name + ".cmd", tool.DiffCmd},
			{"merge.tool", tool.Name},
			{"mergetool." + tool.Name + ".cmd", tool.MergeCmd},
			{"mergetool." + tool.Name + ".trustExitCode", "true"},
		} {
			if err := global(ctx, r, "config", "--global", "--replace-all", kv[0], kv[1]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, key := range []string{"diff.tool", "merge.tool"} {
		if v, err := globalGet(ctx, r, key); err != nil {
			return err
		} else if v == tool.Name {
			if err := global(ctx, r, "config", "--global", "--unset-all", key); err != nil {
				return err
			}
		}
	}
	for _, section := range []string{"difftool." + tool.Name, "mergetool." + tool.Name} {
		// Exit code 128 is a section that is not there.
		if err := global(ctx, r, "config", "--global", "--remove-section", section); err != nil && exitCode(err) != 128 {
			return err
		}
	}
	return nil
}

// globalGet is the value of key in the global config, "" when unset.
func globalGet(ctx context.Context, r *run.Runner, key string) (string, error) {
	res, err := r.Run(ctx, &run.Cmd{Outside: true, WorkTree: home(), Read: true, Args: []string{"config", "--global", "--get", key}})
	if exitCode(err) == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

func global(ctx context.Context, r *run.Runner, args ...string) error {
	_, err := r.Run(ctx, &run.Cmd{Outside: true, WorkTree: home(), Args: args})
	return err
}

// home is where git runs for the global config, which names no
// repository: the user's home, or the root without one.
func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return string(os.PathSeparator)
}

func exitCode(err error) int {
	var e *run.Error
	if errors.As(err, &e) {
		return e.ExitCode
	}
	return 0
}
