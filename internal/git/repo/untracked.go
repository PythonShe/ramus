package repo

import (
	"bytes"
	"context"
	"time"

	"ramus/internal/git/run"
)

// untrackedTimeout bounds the walk UntrackedUnder has git make of a
// folder.
const untrackedTimeout = 30 * time.Second

// UntrackedFiles are the files git does not track under a folder.
type UntrackedFiles struct {
	// Paths are the first of them, in git's order, slash-separated from
	// the top of the work tree.
	Paths []string
	// More counts those past them.
	More int
}

// UntrackedUnder lists the files git does not track under dir, a folder
// of the work tree such as git status names one ("newdir/"), leaving out
// what is ignored as status does. It keeps the first max and counts the
// rest. Off the main thread.
func (r *Repository) UntrackedUnder(ctx context.Context, dir string, max int) (UntrackedFiles, error) {
	if r.WorkTree == "" {
		return UntrackedFiles{}, nil
	}
	l := &nameLines{max: max}
	_, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, Stdout: l,
		Timeout: untrackedTimeout, Env: []string{"GIT_LITERAL_PATHSPECS=1"},
		Args: []string{"ls-files", "-z", "--others", "--exclude-standard", "--", dir}})
	if err != nil {
		return UntrackedFiles{}, err
	}
	return UntrackedFiles{Paths: l.names, More: l.more}, nil
}

// nameLines keeps the first max names of ls-files -z output, as it
// comes, and counts the others.
type nameLines struct {
	max   int
	part  []byte
	names []string
	more  int
}

func (l *nameLines) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			// Past max only the count is wanted, not the name.
			if len(l.names) < l.max {
				l.part = append(l.part, b...)
			}
			break
		}
		if len(l.names) < l.max {
			name := b[:i]
			if len(l.part) > 0 {
				name = append(l.part, name...)
			}
			l.names = append(l.names, string(name))
		} else {
			l.more++
		}
		l.part = l.part[:0]
		b = b[i+1:]
	}
	return n, nil
}
