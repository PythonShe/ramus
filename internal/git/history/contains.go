package history

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/run"
)

// RelationsTimeout bounds the search for what contains a commit and for its
// children, natively or through git: on a large repository without a
// commit-graph, git reads every commit above it.
const RelationsTimeout = 2 * time.Minute

// Relations are what a commit is to the commits tips name: which of them
// reach it, and which commits they reach have it as a parent.
type Relations struct {
	// Reach is set, by tip, for each tip that is the commit or has it as
	// an ancestor.
	Reach []bool
	// Children are the commits with it as a parent that the tips reach,
	// newest commit date first.
	Children [][]byte
}

// RelationsOf finds the relations of commit target to tips in src, reading
// through cat the commits the source does not hold yet, as Resolve does, at
// most limit of them (ErrTooManyRead past it). A tip that is not a commit
// reaches nothing. A target the tips do not reach has no relations.
func RelationsOf(ctx context.Context, src *Source, cat *catfile.Process, target []byte, tips [][]byte, limit int) (Relations, error) {
	rel := Relations{Reach: make([]bool, len(tips))}
	if _, err := src.Resolve(ctx, cat, tips, limit); err != nil {
		return rel, err
	}
	// The source holds every commit the tips reach now: a target it does
	// not hold is not among them.
	t, ok := src.Lookup(target)
	if !ok || !src.known(t) {
		return rel, nil
	}
	ids := make([]uint32, len(tips))
	in := make([]bool, len(tips))
	for i, tip := range tips {
		if id, ok := src.Lookup(tip); ok && src.known(id) {
			ids[i], in[i] = id, true
		}
	}
	reach, children, err := contains(ctx, src, t, ids, in)
	if err != nil {
		return rel, err
	}
	rel.Reach = reach
	for _, c := range children {
		rel.Children = append(rel.Children, bytes.Clone(src.OID(c)))
	}
	return rel, nil
}

// The states of a commit in contains.
const (
	unseen uint8 = iota
	entered
	reachesNot
	reaches
)

// contains reports, for each tip in use, whether it is target or reaches
// it, and the children of target among the commits the tips reach.
//
// It is git's contains_tag_algo: a depth-first walk that settles each
// commit once, from its parents, and keeps the answer, so a thousand tags
// on one history cost one walk of it. A commit of a generation below the
// target's cannot reach it and is not walked; every other commit the tips
// reach is, all of its parents looked at rather than stopping at the first
// that reaches the target, which is how its children come out of the
// same walk.
func contains(ctx context.Context, src *Source, target uint32, tips []uint32, use []bool) ([]bool, []uint32, error) {
	reach := make([]bool, len(tips))
	floor := src.Generation(target)
	if g := src.Graph(); g != nil && !g.HasGenerations() {
		floor = 0 // no generations to cut by: everything is walked
	}
	state := make([]uint8, src.Len())
	var children, stack, ps []uint32
	var err error
	steps := 0
	for i, tip := range tips {
		if !use[i] {
			continue
		}
		if tip == target {
			reach[i] = true
			continue
		}
		if src.Generation(tip) < floor {
			continue
		}
		stack = append(stack[:0], tip)
		for len(stack) > 0 {
			if steps++; steps&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
			}
			x := stack[len(stack)-1]
			switch state[x] {
			case unseen:
				// Its parents go on the stack above it, to be settled
				// before it comes up again.
				state[x] = entered
				if ps, err = src.Parents(x, ps[:0]); err != nil {
					return nil, nil, err
				}
				child := false
				for _, p := range ps {
					if p == target {
						child = true
					} else if state[p] == unseen && src.Generation(p) >= floor {
						stack = append(stack, p)
					}
				}
				if child {
					children = append(children, x)
				}
			case entered:
				stack = stack[:len(stack)-1]
				if ps, err = src.Parents(x, ps[:0]); err != nil {
					return nil, nil, err
				}
				state[x] = reachesNot
				for _, p := range ps {
					// A parent still entered would make a cycle, which
					// only a damaged history has: it reaches nothing.
					if p == target || state[p] == reaches {
						state[x] = reaches
						break
					}
				}
			default:
				// Settled after it was pushed, through another child.
				stack = stack[:len(stack)-1]
			}
		}
		reach[i] = state[tip] == reaches
	}
	slices.SortFunc(children, func(a, b uint32) int {
		if c := cmp.Compare(src.CommitTime(b), src.CommitTime(a)); c != 0 {
			return c
		}
		return bytes.Compare(src.OID(a), src.OID(b))
	})
	return reach, children, nil
}

// RefsContainingGit asks git for the branches, remote branches and tags
// that contain commit target, by full name, for a repository the engine
// cannot walk.
func RefsContainingGit(ctx context.Context, r *run.Runner, gitDir string, target []byte) ([]string, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Timeout: RelationsTimeout,
		Args: []string{"for-each-ref", "--contains", hex.EncodeToString(target), "--format=%(refname)",
			"refs/heads/", "refs/remotes/", "refs/tags/"}})
	if err != nil {
		return nil, err
	}
	var out []string
	for line := range bytes.Lines(res.Stdout) {
		if name := string(bytes.TrimRight(line, "\n")); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// ChildrenGit asks git for the children of commit target among the commits
// tips reach, newest commit date first, for a repository the engine cannot
// walk. Every tip must be a commit. Only the commits target does not reach
// are listed, which its children are among.
func ChildrenGit(ctx context.Context, r *run.Runner, gitDir string, target []byte, tips [][]byte) ([][]byte, error) {
	if len(tips) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	for _, t := range tips {
		fmt.Fprintf(&in, "%x\n", t)
	}
	fmt.Fprintf(&in, "^%x\n", target)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := &childrenParser{target: []byte(hex.EncodeToString(target)), cancel: cancel}
	_, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Stdin: &in, Stdout: p, Timeout: RelationsTimeout,
		Args: []string{"rev-list", "--parents", "--stdin"}})
	if p.err != nil {
		return nil, p.err
	}
	if err != nil {
		return nil, err
	}
	if len(p.partial) > 0 {
		return nil, errors.New("history: rev-list output ends mid-line")
	}
	return p.children, nil
}

// childrenParser keeps the commits of rev-list --parents lines that name
// target as a parent, without holding the rest.
type childrenParser struct {
	target   []byte
	partial  []byte
	children [][]byte
	err      error
	cancel   func()
}

func (p *childrenParser) Write(b []byte) (int, error) {
	n := len(b)
	if p.err != nil {
		return 0, p.err
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if len(p.partial)+len(b) > maxRevListLine {
				p.err = fmt.Errorf("history: rev-list line over %d bytes", maxRevListLine)
				p.cancel()
				return 0, p.err
			}
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

func (p *childrenParser) line(line []byte) error {
	id, parents, _ := bytes.Cut(line, []byte{' '})
	for len(parents) > 0 {
		var h []byte
		h, parents, _ = bytes.Cut(parents, []byte{' '})
		if !bytes.Equal(h, p.target) {
			continue
		}
		oid := make([]byte, len(id)/2)
		if _, err := hex.Decode(oid, id); err != nil || len(id) != len(p.target) {
			return fmt.Errorf("history: rev-list object id %q", id)
		}
		p.children = append(p.children, oid)
		return nil
	}
	return nil
}
