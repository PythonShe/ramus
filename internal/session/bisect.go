package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// BisectState is where a bisect is.
type BisectState struct {
	Active bool
	// Bad and Good are the commits marked (hex); Skipped those skipped.
	Bad     string
	Good    []string
	Skipped []string
	// Left is how many commits are still to test after the one checked
	// out, and Steps roughly how many tests that takes; both are -1
	// before a bad and a good commit are marked.
	Left, Steps int
	// FirstBad is the commit found, once only it is left.
	FirstBad string
}

// LoadBisect reads the bisect in progress off the main thread and hands it
// to done on it.
func (s *Session) LoadBisect(done func(BisectState, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	update := s.update
	r.Life().Go(func(ctx context.Context) {
		st, err := readBisect(ctx, r.Runner, r.GitDir)
		update(func() { done(st, err) })
	})
}

func readBisect(ctx context.Context, rn *run.Runner, gitDir string) (BisectState, error) {
	st := BisectState{Left: -1, Steps: -1}
	if _, err := os.Stat(filepath.Join(gitDir, "BISECT_START")); err != nil {
		return st, nil
	}
	st.Active = true
	res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true,
		Args: []string{"for-each-ref", "--format=%(refname)%00%(objectname)", "refs/bisect/"}})
	if err != nil {
		return st, err
	}
	for line := range bytes.SplitSeq(res.Stdout, []byte{'\n'}) {
		name, id, ok := strings.Cut(string(line), "\x00")
		if !ok {
			continue
		}
		switch {
		case name == "refs/bisect/bad":
			st.Bad = id
		case strings.HasPrefix(name, "refs/bisect/good-"):
			st.Good = append(st.Good, id)
		case strings.HasPrefix(name, "refs/bisect/skip-"):
			st.Skipped = append(st.Skipped, id)
		}
	}
	if st.Bad == "" || len(st.Good) == 0 {
		return st, nil
	}
	args := append([]string{"rev-list", "--bisect-vars", st.Bad, "--not"}, st.Good...)
	res, err = rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: append(args, "--")})
	if err != nil {
		return st, err
	}
	vars := map[string]string{}
	for line := range bytes.SplitSeq(res.Stdout, []byte{'\n'}) {
		k, v, ok := strings.Cut(string(line), "=")
		if ok {
			vars[k] = strings.Trim(v, "'")
		}
	}
	all, err1 := strconv.Atoi(vars["bisect_all"])
	left, err2 := strconv.Atoi(vars["bisect_nr"])
	steps, err3 := strconv.Atoi(vars["bisect_steps"])
	if err := errors.Join(err1, err2, err3); err != nil {
		return st, errors.New("session: git rev-list --bisect-vars said " + strconv.Quote(string(res.Stdout)))
	}
	st.Left, st.Steps = left, steps
	if all <= 1 {
		st.FirstBad, st.Left, st.Steps = st.Bad, 0, 0
	}
	return st, nil
}
