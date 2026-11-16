package repo

import (
	"bytes"
	"context"
	"errors"
	"time"

	"ramus/internal/git/run"
)

// ignoredBudget bounds the names IgnoredDirs keeps: past it the rest are
// left out, and only watched where they could have been spared.
const ignoredBudget = 1 << 20

// ignoredTimeout bounds each git command IgnoredDirs runs.
const ignoredTimeout = 30 * time.Second

// IgnoredDirs lists the work tree's ignored directories, as git sees
// them: the topmost of each ignored tree, slash-separated from the top of
// the work tree, without the trailing slash. Off the main thread.
func (r *Repository) IgnoredDirs(ctx context.Context) ([]string, error) {
	if r.WorkTree == "" {
		return nil, nil
	}
	// ls-files lists ignored files too, which are not kept, and a folder
	// all of whose files are ignored though no rule ignores it: a file
	// made there later is not, so check-ignore keeps only the folders a
	// rule names.
	found := &dirLines{}
	_, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, Stdout: found,
		Timeout: ignoredTimeout,
		Args:    []string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"}})
	if err != nil || len(found.dirs) == 0 {
		return nil, err
	}
	var in bytes.Buffer
	for _, p := range found.dirs {
		in.WriteString(p)
		in.WriteString("/\x00")
	}
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, Stdin: &in,
		Timeout: ignoredTimeout, MaxOutput: 2 * ignoredBudget,
		Args: []string{"check-ignore", "-z", "--stdin"}})
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return nil, nil // none is
	}
	if err != nil {
		return nil, err
	}
	ruled := &dirLines{}
	ruled.Write(res.Stdout)
	return ruled.dirs, nil
}

// dirLines keeps the directories of ls-files -z output, as it comes, up
// to ignoredBudget bytes of names.
type dirLines struct {
	part []byte
	// long is set while a name past any path's length is skipped.
	long bool
	dirs []string
	n    int
}

func (d *dirLines) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			d.long = d.long || len(d.part)+len(b) > 4096
			if !d.long {
				d.part = append(d.part, b...)
			}
			break
		}
		name, long := b[:i], d.long || len(d.part)+i > 4096
		if len(d.part) > 0 && !long {
			name = append(d.part, name...)
		}
		d.part, d.long = d.part[:0], false
		b = b[i+1:]
		if !long && len(name) > 1 && name[len(name)-1] == '/' && d.n+len(name) <= ignoredBudget {
			d.dirs = append(d.dirs, string(name[:len(name)-1]))
			d.n += len(name)
		}
	}
	return n, nil
}
