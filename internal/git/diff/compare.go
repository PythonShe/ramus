package diff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// DefaultMaxCompared bounds the listing of a folder comparison, in bytes.
const DefaultMaxCompared = 16 << 20

// Compared is what differs between two folders, as git diff --no-index
// lists it: each entry's status and its path under each side, relative,
// held in one buffer.
type Compared struct {
	buf    []byte
	status []byte
	// spans holds an old and a new path per entry; an empty span is a
	// side the entry is not on.
	spans []Span
	// Truncated is set when the listing was cut off at its limit.
	Truncated bool
}

// Len returns the number of entries.
func (c *Compared) Len() int { return len(c.status) }

// Status returns entry i's status letter: A, D, M, R, C or T.
func (c *Compared) Status(i int) byte { return c.status[i] }

// Old returns entry i's path under the old folder, "" when it is not
// there.
func (c *Compared) Old(i int) string { return c.span(2 * i) }

// New returns entry i's path under the new folder, "" when it is not
// there.
func (c *Compared) New(i int) string { return c.span(2*i + 1) }

func (c *Compared) span(k int) string {
	s := c.spans[k]
	return string(c.buf[s.Start:s.End])
}

// Bytes is about what the listing holds in memory.
func (c *Compared) Bytes() int { return len(c.buf) + len(c.status) + 8*len(c.spans) }

// compareCmd runs git diff --no-index outside any repository, so that no
// repository's config or attributes apply; the user's own config does.
func compareCmd(args []string, stdout *capped) *run.Cmd {
	return &run.Cmd{
		Outside: true, WorkTree: "/", Read: true, Stdout: stdout,
		Args:   append([]string{"diff", "--no-index", "--no-color", "--no-ext-diff"}, args...),
		Config: []string{"core.quotePath=false"},
	}
}

// CompareFolders lists what differs between the folders old and new,
// both absolute, with renames found.
func CompareFolders(ctx context.Context, r *run.Runner, old, new string, max int) (*Compared, error) {
	if max <= 0 {
		max = DefaultMaxCompared
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &capped{max: max, cancel: cancel}
	_, err := r.Run(ctx, compareCmd([]string{"--name-status", "-z", "-M", "--", old, new}, w))
	if err = exitOne(err); err != nil && !w.full {
		return nil, err
	}
	c, err := parseCompared(w.buf, old, new)
	if err != nil {
		return nil, err
	}
	c.Truncated = w.full
	return c, nil
}

// exitOne drops the exit status 1 that git diff --no-index gives when the
// sides differ.
func exitOne(err error) error {
	var e *run.Error
	if errors.As(err, &e) && e.ExitCode == 1 {
		return nil
	}
	return err
}

// parseCompared reads git's --name-status -z listing of old and new; an
// entry cut off at the end is left out.
func parseCompared(data []byte, old, new string) (*Compared, error) {
	c := &Compared{}
	rel := func(p []byte, root string) ([]byte, error) {
		if rest, ok := bytes.CutPrefix(p, []byte(strings.TrimSuffix(root, "/")+"/")); ok {
			return rest, nil
		}
		if string(p) == root {
			return nil, nil
		}
		return nil, fmt.Errorf("diff: %q is not under %q", p, root)
	}
	add := func(p []byte) Span {
		start := uint32(len(c.buf))
		c.buf = append(c.buf, p...)
		return Span{start, uint32(len(c.buf))}
	}
	for len(data) > 0 {
		st, rest, ok := bytes.Cut(data, []byte{0})
		if !ok || len(st) == 0 {
			break
		}
		kind := st[0]
		n := 1
		if kind == 'R' || kind == 'C' {
			n = 2
		}
		var paths [][]byte
		for range n {
			var p []byte
			if p, rest, ok = bytes.Cut(rest, []byte{0}); !ok {
				break
			}
			paths = append(paths, p)
		}
		if len(paths) < n {
			break
		}
		if _, err := strconv.Atoi(string(st[1:])); len(st) > 1 && err != nil {
			return nil, fmt.Errorf("diff: bad status %q", st)
		}
		var o, nw Span
		var err error
		var p []byte
		switch kind {
		case 'A':
			p, err = rel(paths[0], new)
			nw = add(p)
		case 'D':
			p, err = rel(paths[0], old)
			o = add(p)
		case 'M', 'T':
			// One path, the old side's; the new side has the same name.
			p, err = rel(paths[0], old)
			o = add(p)
			nw = o
		case 'R', 'C':
			if p, err = rel(paths[0], old); err == nil {
				o = add(p)
				p, err = rel(paths[1], new)
				nw = add(p)
			}
		default:
			return nil, fmt.Errorf("diff: bad status %q", st)
		}
		if err != nil {
			return nil, err
		}
		c.status = append(c.status, kind)
		c.spans = append(c.spans, o, nw)
		data = rest
	}
	return c, nil
}

// ComparePatch is the patch from file old to file new, absolute paths,
// either "" for a side that does not exist.
func ComparePatch(ctx context.Context, r *run.Runner, old, new string, ws Whitespace, max int) (Patch, error) {
	if max <= 0 {
		max = DefaultMaxPatch
	}
	// git quotes a name holding a backslash, so on Windows every header
	// would be quoted; it takes forward slashes there too.
	old, new = filepath.ToSlash(old), filepath.ToSlash(new)
	if old == "" {
		old = "/dev/null"
	}
	if new == "" {
		new = "/dev/null"
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &capped{max: max, cancel: cancel}
	args := append(ws.args(), "--", old, new)
	_, err := r.Run(ctx, compareCmd(args, w))
	if w.full {
		return Patch{Text: w.cut(), Truncated: true}, nil
	}
	if err = exitOne(err); err != nil {
		return Patch{}, err
	}
	return Patch{Text: w.buf}, nil
}
