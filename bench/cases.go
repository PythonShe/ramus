package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/history"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/session"
)

// benchCase is one benchmark: run measures it once.
type benchCase struct {
	name, what string
	run        func(m *meter) error
}

var cases = []benchCase{
	{"history-kernel", "open the Linux kernel: refs, first screen of history, the whole graph", historyKernel(false)},
	{"history-kernel-no-graph", "the same with core.commitGraph=false, as a repository without a commit-graph", historyKernel(true)},
	{"filter-kernel", "filter the kernel's history by message, author and path, to the first screen and to the end", filterKernel},
	{"status-monorepo", "open a 390k-file repository to a populated status, without fsmonitor", statusMonorepo},
	{"touch-20k", "one file saved in an open repository of 20,000 files, to the status showing it", touchOne},
	{"scroll-kernel", "scroll the kernel's graph of 1.3M commits; frame times; jump to its first commit", scrollKernel},
	{"diff-large", "a 50 MB text file's patch, git's part and the viewer's apart, and a 100 MB binary's", bigDiff},
	{"commit-10k", "the file list of a commit of 10,000 files", commit10k},
	{"save-5000", "5,000 files saved at once in an open repository: frame times, peak git processes", save5000},
	{"idle-10", "CPU and memory with 10 repositories open and nothing happening", idle10},
	{"refs-50k", "a repository of 50,000 branches, packed: refs, status, branches view, history", refs50k},
	{"packs-2000", "a repository of 2,000 packs and 5,000 loose objects", packs2000},
	{"fallbacks", "reftable, SHA-256, partial-clone and shallow repositories, beside a plain one", fallbacks},
	{"soak", "open, change, refresh and close repositories for RAMUS_SOAK; goroutines, files, processes and memory return", soak},
}

// meter collects one run's times and facts.
type meter struct {
	times []named
	facts map[string]string
	skip  string
}

var errSkip = errors.New("skipped")

func (m *meter) time(name string, d time.Duration) { m.times = append(m.times, named{name, d}) }
func (m *meter) fact(k string, v any)              { m.facts[k] = fmt.Sprint(v) }
func (m *meter) skipf(format string, a ...any) error {
	m.skip = fmt.Sprintf(format, a...)
	return errSkip
}

// fixture is a repository in RAMUS_FIXTURES.
func (m *meter) fixture(name string) (string, error) {
	root := os.Getenv("RAMUS_FIXTURES")
	if root == "" {
		return "", m.skipf("RAMUS_FIXTURES is not set")
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err != nil {
		return "", m.skipf("no fixture %s", name)
	}
	return dir, nil
}

// maxProcesses is the app's limit on git processes at once.
const maxProcesses = 8

// open opens a session of dir with the user's environment and more, and
// returns it with what runs its updates, as the main thread does.
func open(dir string, more ...string) (*session.Session, *pump, error) {
	env := append(os.Environ(), more...)
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		return nil, nil, err
	}
	ts, err := trust.Load(filepath.Join(os.TempDir(), fmt.Sprintf("ramus-bench-trust-%d.json", os.Getpid())))
	if err != nil {
		return nil, nil, err
	}
	p := &pump{updates: make(chan func(), 4096)}
	s := session.Open(run.New(g, env, maxProcesses), ts, dir, func(fn func()) { p.updates <- fn })
	p.s = s
	return s, p, nil
}

// pump runs the session's updates, as the app's main thread does.
type pump struct {
	s       *session.Session
	updates chan func()
}

// until runs updates until ok, within d.
func (p *pump) until(what string, d time.Duration, ok func() bool) error {
	deadline := time.After(d)
	for !ok() {
		select {
		case fn := <-p.updates:
			fn()
		case <-deadline:
			return fmt.Errorf("timed out after %v waiting for %s", d, what)
		}
	}
	return nil
}

func (p *pump) close() {
	p.s.Close()
	for {
		select {
		case <-p.s.Done():
			return
		case fn := <-p.updates:
			fn()
		}
	}
}

func historyKernel(noGraph bool) func(m *meter) error {
	return func(m *meter) error {
		dir, err := m.fixture("linux")
		if err != nil {
			return err
		}
		var more []string
		if noGraph {
			cfg := filepath.Join(os.TempDir(), fmt.Sprintf("ramus-bench-nograph-%d", os.Getpid()))
			if err := os.WriteFile(cfg, []byte("[core]\n\tcommitGraph = false\n"), 0o644); err != nil {
				return err
			}
			defer os.Remove(cfg)
			more = append(more, "GIT_CONFIG_GLOBAL="+cfg)
		}
		start := time.Now()
		s, p, err := open(dir, more...)
		if err != nil {
			return err
		}
		defer p.close()
		if err := p.until("the refs", time.Minute, func() bool { return s.State.Refs != nil }); err != nil {
			return err
		}
		m.time("open to refs read", time.Since(start))
		if noGraph && s.Repo.Caps.CommitGraphUsable() {
			return errors.New("the commit-graph is still in use")
		}
		return loadHistory(m, s, p, start)
	}
}

// loadHistory loads the history of s and times its first screen and its
// end, from start.
func loadHistory(m *meter, s *session.Session, p *pump, start time.Time) error {
	t0 := time.Now()
	s.LoadHistory()
	first := time.Duration(0)
	err := p.until("the history", 10*time.Minute, func() bool {
		if first == 0 && s.History.Snap != nil && s.History.Snap.Len() > 0 {
			first = time.Since(t0)
			m.time("first screen", first)
			m.time("open to first screen", time.Since(start))
		}
		return s.History.Done
	})
	if err != nil {
		return err
	}
	h := s.History
	if h.Err != nil {
		return h.Err
	}
	m.time("whole history", time.Since(t0))
	m.fact("rows", h.Snap.Len())
	m.fact("native", h.Native)
	return nil
}

func filterKernel(m *meter) error {
	dir, err := m.fixture("linux")
	if err != nil {
		return err
	}
	s, p, err := open(dir)
	if err != nil {
		return err
	}
	defer p.close()
	if err := p.until("the refs", time.Minute, func() bool { return s.State.Refs != nil }); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		f    history.Filter
	}{
		{"message", history.Filter{Message: "use-after-free"}},
		{"author", history.Filter{Author: "torvalds@linux-foundation.org"}},
		{"path", history.Filter{Paths: []string{"kernel/sched/core.c"}}},
	} {
		// The history shown is the last filter's until this one's comes.
		before := s.History.View
		s.SetHistoryView(session.HistoryView{Filter: f.f})
		t0 := time.Now()
		s.LoadHistory()
		first := time.Duration(0)
		err := p.until("the filtered history", 10*time.Minute, func() bool {
			h := s.History
			if h.View == before || !h.Filtered {
				return false
			}
			if first == 0 && h.Snap != nil && h.Snap.Len() > 0 {
				first = time.Since(t0)
				m.time(f.name+": first screen", first)
			}
			return h.Done
		})
		if err != nil {
			return err
		}
		if s.History.Err != nil {
			return s.History.Err
		}
		m.time(f.name+": all found", time.Since(t0))
		m.fact(f.name+" rows", s.History.Snap.Len())
	}
	return nil
}

func statusMonorepo(m *meter) error {
	dir, err := m.fixture("monorepo.noindex")
	if err != nil {
		return err
	}
	start := time.Now()
	s, p, err := open(dir)
	if err != nil {
		return err
	}
	defer p.close()
	if err := p.until("the status", 5*time.Minute, func() bool { return s.State.Status != nil }); err != nil {
		return err
	}
	m.time("open to status", time.Since(start))
	m.fact("entries", s.State.Status.Len())
	if s.Repo != nil {
		v, _ := s.Repo.Config().Get("core.fsmonitor")
		m.fact("core.fsmonitor", fmt.Sprintf("%q", v))
	}
	return nil
}
