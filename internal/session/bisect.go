package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
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
	// New and Old are the words the bisect marks commits with, for bad
	// and good: "bad" and "good" unless it was started with others, as
	// git bisect start --term-new=broken --term-old=fixed does, which git
	// bisect bad and good then refuse.
	New, Old string
	// OnlySkipped is set when every commit left to test was skipped.
	OnlySkipped bool
	// Testing is the commit under test (hex): HEAD's, or BISECT_HEAD's
	// for a bisect started with --no-checkout, which leaves HEAD where it
	// was; "" when there is neither.
	Testing string
}

// OwnTerms reports a bisect marked with words other than bad and good.
func (b BisectState) OwnTerms() bool { return b.Active && (b.New != "bad" || b.Old != "good") }

// Term is the word that marks a commit v in this bisect: its own term
// for bad or good, when it has them.
func (b BisectState) Term(v write.BisectVerdict) write.BisectVerdict {
	if !b.OwnTerms() {
		return v
	}
	return write.BisectTerm(v, b.New, b.Old)
}

// LoadBisect reads the bisect in progress off the main thread and hands it
// to done on it; none, without an error, when the repository closed and
// opened again (Trust) before it was handed over.
func (s *Session) LoadBisect(done func(BisectState, error)) {
	r := s.Repo
	if r == nil {
		return
	}
	r.Life().Go(func(ctx context.Context) {
		st, err := readBisect(ctx, r.Runner, r.GitDir)
		// A read of the repository before it opened again shows no
		// bisect: the bar reads it again (Writes).
		s.handOver(r, func() { done(st, err) }, func() { done(BisectState{Left: -1, Steps: -1}, nil) })
	})
}

func readBisect(ctx context.Context, rn *run.Runner, gitDir string) (BisectState, error) {
	st := BisectState{Left: -1, Steps: -1}
	if _, err := os.Stat(filepath.Join(gitDir, "BISECT_START")); err != nil {
		return st, nil
	}
	st.Active = true
	st.New, st.Old = "bad", "good"
	// BISECT_TERMS holds the new term, then the old one. A regular file
	// only: a named pipe there would hold the read for ever.
	if b, err := pathsafe.ReadFile(filepath.Join(gitDir, "BISECT_TERMS"), 1023); err == nil {
		if lines := strings.Fields(string(b)); len(lines) == 2 {
			st.New, st.Old = lines[0], lines[1]
		}
	}
	// Read with the rest, so a mark acts on the commit the state shown
	// was read with.
	for _, rev := range []string{"BISECT_HEAD^{commit}", "HEAD^{commit}"} {
		res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: []string{"rev-parse", "-q", "--verify", rev}})
		var e *run.Error
		if err == nil {
			st.Testing = strings.TrimSpace(string(res.Stdout))
			break
		}
		if !errors.As(err, &e) || e.ExitCode != 1 {
			return st, err
		}
	}
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
		case name == "refs/bisect/"+st.New:
			st.Bad = id
		case strings.HasPrefix(name, "refs/bisect/"+st.Old+"-"):
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
		return st, nil
	}
	// rev-list knows nothing of skipped commits: when they are all that
	// is left besides the bad one, git has nothing more to check out.
	if all <= len(st.Skipped)+1 {
		args := append([]string{"rev-list", st.Bad, "--not"}, st.Good...)
		res, err := rn.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Args: append(args, "--")})
		if err != nil {
			return st, err
		}
		only := true
		for id := range strings.FieldsSeq(string(res.Stdout)) {
			if id != st.Bad && !slices.Contains(st.Skipped, id) {
				only = false
				break
			}
		}
		st.OnlySkipped = only
	}
	return st, nil
}
