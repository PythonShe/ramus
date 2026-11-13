package write

import (
	"context"
	"errors"
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
}

// ErrDirNotEmpty is a clone into a directory that holds files.
var ErrDirNotEmpty = errors.New("write: the folder to clone into is not empty")

// Clone starts a clone at once, outside any queue since there is no
// repository yet. It never recurses into submodules: their URLs come from
// the repository just cloned, and are offered once the user trusts it.
func Clone(r *run.Runner, opt CloneOptions) (*Job, error) {
	if err := redact.CheckRemoteURL(opt.URL); err != nil {
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
	args := []string{"clone", "--progress", "--no-recurse-submodules"}
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
	return start(r, Op{Name: "Clone " + redact.Secrets(opt.URL), Cmd: run.Cmd{Outside: true, WorkTree: parent, Args: args, Timeout: CloneTimeout}}), nil
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

// start runs op now, on its own.
func start(r *run.Runner, op Op) *Job {
	j := newJob(nil, op)
	go func() {
		res, err := execute(j.ctx, r, j)
		j.finish(res, err)
	}()
	return j
}
