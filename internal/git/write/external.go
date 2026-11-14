package write

import (
	"context"
	"io"

	"ramus/internal/git/run"
)

// External is a write another tool of the user's makes, running git
// itself, as gh stack does: it takes its turn in the queue as git's
// writes do, so none runs beside it, its output goes to the job's events,
// and cancelling the job cancels ctx. line is the command as the user
// would type it, for the log. It is not undone: what the tool changed is
// the tool's.
func External(w Where, name, line string, fn func(ctx context.Context, stdout, stderr io.Writer) error) Op {
	return Op{Name: name, Line: line, Cmd: w.cmd(), do: func(ctx context.Context, j *Job) (run.Result, error) {
		out, errw := &lines{j: j, stream: Stdout}, &lines{j: j, stream: Stderr}
		stdout, stderr := &keep{max: maxStdout}, &keep{max: 64 << 10}
		err := fn(ctx, teeWriter{stdout, out}, teeWriter{stderr, errw})
		out.flush()
		errw.flush()
		return run.Result{Stdout: stdout.buf, Stderr: string(stderr.buf)}, err
	}}
}
