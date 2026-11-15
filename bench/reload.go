package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ramus/internal/git/repo"
	"ramus/internal/session"
)

// kernelClone makes a bare clone of the kernel fixture that shares its
// objects, with its commit-graph linked in: commits made in it are the
// clone's, and the fixture is only read.
func kernelClone(m *meter) (string, error) {
	fix, err := m.fixture("linux")
	if err != nil {
		return "", err
	}
	return made("kernel-clone", func(dir string) error {
		repo := filepath.Join(dir, "repo.git")
		if _, err := git(dir, nil, "clone", "-q", "--bare", "--shared", fix, repo); err != nil {
			return err
		}
		from := filepath.Join(fix, ".git", "objects", "info")
		to := filepath.Join(repo, "objects", "info")
		for _, name := range []string{"commit-graph", "commit-graphs"} {
			if err := linkTree(filepath.Join(from, name), filepath.Join(to, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
}

// linkTree hard-links the file or folder from at to, copying where a link
// cannot be made. git replaces commit-graph files by renaming, never
// writing into one, so a link leaves the fixture's as they are.
func linkTree(from, to string) error {
	fi, err := os.Stat(from)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if err := os.Link(from, to); err == nil {
			return nil
		}
		in, err := os.Open(from)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(to)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := linkTree(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// commitOn makes n commits on ref through fast-import, the first on top
// of from, each keeping its parent's tree, dated now; it returns the last.
func commitOn(dir, ref, from string, n int, what string) (string, error) {
	var b bytes.Buffer
	when := time.Now().Unix()
	for i := range n {
		msg := fmt.Sprintf("%s %d", what, i)
		fmt.Fprintf(&b, "commit %s\ncommitter Bench <bench@example.com> %d +0000\ndata %d\n%s\n", ref, when, len(msg), msg)
		if i == 0 {
			fmt.Fprintf(&b, "from %s\n", from)
		}
		b.WriteString("\n")
	}
	if _, err := git(dir, b.Bytes(), "fast-import", "--quiet", "--force"); err != nil {
		return "", err
	}
	out, err := git(dir, nil, "rev-parse", ref)
	return strings.TrimSpace(out), err
}

// historyKernelReload times history as commits land in the kernel: a
// load with a commit-graph missing the newest commits, a commit on top,
// and a fetch adding 100 commits on top.
func historyKernelReload(m *meter) error {
	dir, err := kernelClone(m)
	if err != nil {
		return err
	}
	return reloadTimes(m, filepath.Join(dir, "repo.git"), "master")
}

// history100k is the same on a made repository of 100,000 commits with a
// commit-graph.
func history100k(m *meter) error {
	dir, err := made("history-100k", func(dir string) error {
		if err := fastImport(dir, 100000, 2000, 2); err != nil {
			return err
		}
		_, err := git(repoDir(dir), nil, "commit-graph", "write", "--reachable", "--split", "--changed-paths")
		return err
	})
	if err != nil {
		return err
	}
	return reloadTimes(m, repoDir(dir), "main")
}

func reloadTimes(m *meter, dir, branch string) error {
	head := "refs/heads/" + branch
	remote := "refs/remotes/origin/" + branch
	base, err := git(dir, nil, "rev-parse", "--verify", "refs/bench/base")
	if err != nil {
		// The first run marks where the branch was, for later runs to
		// start from there again.
		if base, err = git(dir, nil, "rev-parse", head); err != nil {
			return err
		}
		if _, err := git(dir, nil, "update-ref", "refs/bench/base", strings.TrimSpace(base)); err != nil {
			return err
		}
	}
	base = strings.TrimSpace(base)
	if _, err := git(dir, nil, "update-ref", remote, base); err != nil {
		return err
	}
	// Ten commits the commit-graph does not have yet.
	if _, err := commitOn(dir, head, base, 10, "not in the graph"); err != nil {
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
	t0 := time.Now()
	s.LoadHistory()
	first := time.Duration(0)
	if err := p.until("the history", 10*time.Minute, func() bool {
		if first == 0 && s.History.Snap != nil && s.History.Snap.Len() > 0 {
			first = time.Since(t0)
		}
		return s.History.Done
	}); err != nil {
		return err
	}
	if s.History.Err != nil {
		return s.History.Err
	}
	m.time("graph 10 behind: first screen", first)
	m.time("graph 10 behind: whole history", time.Since(t0))
	m.fact("rows", s.History.Snap.Len())
	m.fact("native with the graph behind", s.History.Native)
	if err := settle(s, p); err != nil {
		return err
	}

	tip, err := git(dir, nil, "rev-parse", head)
	if err != nil {
		return err
	}
	one, err := commitOn(dir, head, strings.TrimSpace(tip), 1, "on top")
	if err != nil {
		return err
	}
	if err := shownOnTop(m, "a commit on top", s, p, one); err != nil {
		return err
	}
	if err := settle(s, p); err != nil {
		return err
	}
	last, err := commitOn(dir, remote, one, 100, "fetched")
	if err != nil {
		return err
	}
	return shownOnTop(m, "a fetch of 100 commits", s, p, last)
}

// shownOnTop times the refs changing to history showing oid first, the
// refs read again and the whole history laid out.
func shownOnTop(m *meter, what string, s *session.Session, p *pump, oid string) error {
	t0 := time.Now()
	s.Refresh(repo.RefsChanged)
	err := p.until(what, 10*time.Minute, func() bool {
		s.LoadHistory()
		h := s.History
		return h.Done && h.For == s.State.Refs && h.Snap != nil && h.Snap.Len() > 0 && hex.EncodeToString(h.OID(h.Snap.Commit(0))) == oid
	})
	if err != nil {
		return err
	}
	m.time(what+": shown", time.Since(t0))
	return nil
}

// settle waits for the history to be loaded for the refs as they are and
// for no write to be running, two seconds on end, so that one step's
// work is not timed in the next.
func settle(s *session.Session, p *pump) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		// Nothing may come for the pump to run: wake it to look again.
		for {
			select {
			case <-tick.C:
				select {
				case p.updates <- func() {}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	var quiet time.Time
	return p.until("quiet", 10*time.Minute, func() bool {
		s.LoadHistory()
		h := s.History
		switch idle := h.Done && h.For == s.State.Refs && (s.Queue == nil || s.Queue.Pending() == 0); {
		case !idle:
			quiet = time.Time{}
		case quiet.IsZero():
			quiet = time.Now()
		}
		return !quiet.IsZero() && time.Since(quiet) > 2*time.Second
	})
}
