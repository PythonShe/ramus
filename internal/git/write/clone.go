package write

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"ramus/internal/git/redact"
	"ramus/internal/git/run"
)

// CloneOptions describe a clone.
type CloneOptions struct {
	URL string
	// Dir is the new work tree; it must not exist, or be empty.
	Dir    string
	Branch string
	// Depth makes a shallow clone of that many commits; 0 clones all.
	Depth int
	Bare  bool
	// Submodules clones the submodules too, when the user asks: their URLs
	// come from the repository being cloned.
	Submodules bool
}

// ErrDirNotEmpty is a clone into a directory that holds files.
var ErrDirNotEmpty = errors.New("write: the folder to clone into is not empty")

// Clone starts a clone at once, outside any queue since there is no
// repository yet. It recurses into submodules only when asked.
func Clone(r *run.Runner, opt CloneOptions) (*Job, error) {
	if err := CheckRemoteURL(opt.URL); err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(opt.Dir)
	if err != nil {
		return nil, err
	}
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		return nil, ErrDirNotEmpty
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	// made is a folder the clone itself creates, which a stopped clone
	// removes; one that was there empty is only emptied.
	made := false
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return nil, err
		}
		made = true
	}
	args := []string{"clone", "--progress", "--no-recurse-submodules"}
	if opt.Submodules {
		args[2] = "--recurse-submodules"
	}
	if opt.Branch != "" {
		args = append(args, "--branch", opt.Branch)
	}
	if opt.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(opt.Depth))
	}
	if opt.Bare {
		args = append(args, "--bare")
	}
	args = append(args, "--", opt.URL, dir)
	op := Op{Name: "Clone " + redact.Secrets(opt.URL), Cmd: run.Cmd{Outside: true, WorkTree: parent, Args: args, Timeout: CloneTimeout, Stall: NetworkStall}}
	op.do = func(ctx context.Context, j *Job) (run.Result, error) {
		res, err := execute(ctx, r, j, j.Op.Cmd, nil)
		// git removes a clone that fails on its own while fetching, and
		// keeps one whose checkout failed, saying so. Stopped, it keeps
		// what it made once the fetch is done, and on Windows, where it
		// is ended at once, all of it: the folder would be left half
		// made, and a clone there again refused.
		var e *run.Error
		switch {
		case err == nil:
		case ctx.Err() != nil || errors.As(err, &e) && e.ExitCode == -1:
			if cerr := unclone(dir, made); cerr != nil {
				return res, fmt.Errorf("write: the clone stopped, and what it made in %s could not be removed: %w", dir, cerr)
			}
		case made:
			// git empties a folder it was given when its fetch fails;
			// the folder is the clone's own.
			os.Remove(dir)
		}
		return res, err
	}
	return start(r, op), nil
}

// unclone removes what a clone stopped part way made in dir: the folder
// itself when the clone made it, else what it holds, as it was empty.
func unclone(dir string, made bool) error {
	if made {
		return os.RemoveAll(dir)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
	}
	return errors.Join(errs...)
}

// CloneTimeout bounds a clone; a kernel over a slow link takes a while.
const CloneTimeout = 6 * time.Hour

// Init creates a repository at dir.
func Init(ctx context.Context, r *run.Runner, dir string, bare bool) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	args := []string{"init", "-q"}
	if bare {
		args = append(args, "--bare")
	}
	_, err = r.Run(ctx, &run.Cmd{Outside: true, WorkTree: dir, Args: append(args, "--", dir)})
	return err
}

// start runs op now, on its own. A panic ends the job with an error, not
// the app, and whoever waits on the job hears it ended.
func start(r *run.Runner, op Op) *Job {
	j := newJob(nil, op)
	go func() {
		var res run.Result
		var err error
		defer func() {
			if v := recover(); v != nil {
				res, err = run.Result{}, fmt.Errorf("write: %s stopped on an internal error: %v", j.Op.Name, v)
			}
			j.finish(res, err)
		}()
		if j.Op.do != nil {
			res, err = j.Op.do(j.ctx, j)
			return
		}
		res, err = execute(j.ctx, r, j, j.Op.Cmd, j.Op.Input)
	}()
	return j
}
