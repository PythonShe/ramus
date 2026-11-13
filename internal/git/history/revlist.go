package history

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"ramus/internal/git/commits"
	"ramus/internal/git/run"
)

// RevListTimeout bounds the walk git does when no commit-graph helps it: on
// a large repository it reads every commit before printing the first.
const RevListTimeout = 15 * time.Minute

// RevList orders history with git, for a repository whose commit-graph is
// absent, stale or switched off (grafts, replace refs, shallow): it streams
// `git rev-list --parents --timestamp --topo-order` from tips, numbers
// each commit in s and calls fn in display order, each commit after all of
// its children, as TopoWalk does from the graph. parents is reused by the
// next call. An error from fn stops git and is returned.
func RevList(ctx context.Context, r *run.Runner, gitDir string, tips [][]byte, s *commits.Store, fn func(id uint32, parents []uint32) error) error {
	if len(tips) == 0 {
		return nil
	}
	var in bytes.Buffer
	for _, t := range tips {
		fmt.Fprintf(&in, "%x\n", t)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := &revListParser{s: s, fn: fn, oid: make([]byte, 64), cancel: cancel}
	_, err := r.Run(ctx, &run.Cmd{
		GitDir:  gitDir,
		Args:    []string{"rev-list", "--parents", "--timestamp", "--topo-order", "--stdin"},
		Read:    true,
		Stdin:   &in,
		Stdout:  p,
		Timeout: RevListTimeout,
	})
	if p.err != nil {
		return p.err
	}
	if err != nil {
		return err
	}
	if len(p.partial) > 0 {
		return fmt.Errorf("history: rev-list output ends mid-line")
	}
	return nil
}

type revListParser struct {
	s       *commits.Store
	fn      func(uint32, []uint32) error
	partial []byte
	parents []uint32
	oid     []byte
	err     error
	cancel  func()
}

func (p *revListParser) Write(b []byte) (int, error) {
	n := len(b)
	if p.err != nil {
		return 0, p.err
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			p.partial = append(p.partial, b...)
			break
		}
		line := b[:i]
		if len(p.partial) > 0 {
			p.partial = append(p.partial, line...)
			line = p.partial
		}
		if err := p.line(line); err != nil {
			p.err = err
			p.cancel()
			return 0, err
		}
		p.partial = p.partial[:0]
		b = b[i+1:]
	}
	return n, nil
}

func (p *revListParser) line(line []byte) error {
	field := func() []byte {
		f, rest, _ := bytes.Cut(line, []byte{' '})
		line = rest
		return f
	}
	ts, err := strconv.ParseInt(string(field()), 10, 64)
	if err != nil {
		return fmt.Errorf("history: rev-list timestamp: %w", err)
	}
	id, err := p.id(field())
	if err != nil {
		return err
	}
	p.parents = p.parents[:0]
	for len(line) > 0 {
		pid, err := p.id(field())
		if err != nil {
			return err
		}
		p.parents = append(p.parents, pid)
	}
	if err := p.s.Add(id, p.parents, ts); err != nil {
		return err
	}
	return p.fn(id, p.parents)
}

func (p *revListParser) id(h []byte) (uint32, error) {
	if len(h) != 2*p.s.Width() {
		return 0, fmt.Errorf("history: rev-list object id %q", h)
	}
	oid := p.oid[:len(h)/2]
	if _, err := hex.Decode(oid, h); err != nil {
		return 0, fmt.Errorf("history: rev-list object id: %w", err)
	}
	return p.s.ID(oid), nil
}
