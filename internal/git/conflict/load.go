package conflict

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"

	"ramus/internal/git/run"
)

// Load splits data, the work tree's text of path, a file git left
// conflicted, into segments: it reads markers of the size git wrote them
// in, from the file's conflict-marker-size attribute, and tells text that
// only looks like a conflict from git's by merging the sides in the index
// again (see remerge).
func Load(ctx context.Context, r *run.Runner, gitDir, workTree, path string, data []byte) ([]Segment, error) {
	if len(data) > MaxSize {
		return nil, errLarge
	}
	size, err := markerSize(ctx, r, gitDir, workTree, path)
	if err != nil {
		return nil, err
	}
	if !hasOpen(data, size) {
		return ParseSize(data, size)
	}
	s, err := readSides(ctx, r, gitDir, workTree, path)
	if err != nil {
		return nil, err
	}
	if !s.holdsMarkers(size) {
		return ParseSize(data, size)
	}
	return remerge(ctx, r, data, size, s)
}

// markerSize is the size of the conflict markers git writes for path, as
// its conflict-marker-size attribute sets it.
func markerSize(ctx context.Context, r *run.Runner, gitDir, workTree, path string) (int, error) {
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true,
		Args: []string{"check-attr", "-z", "conflict-marker-size", "--", path}})
	if err != nil {
		return 0, err
	}
	// path NUL attribute NUL value NUL
	f := bytes.Split(res.Stdout, []byte{0})
	if len(f) < 3 {
		return 0, fmt.Errorf("conflict: check-attr printed %q", res.Stdout)
	}
	// git's rule: a positive number, else the default.
	if n, err := strconv.Atoi(string(f[2])); err == nil && n > 0 {
		return n, nil
	}
	return DefaultMarkerSize, nil
}

func hasOpen(data []byte, size int) bool {
	for pos := 0; pos < len(data); {
		line, next := lineAt(data, pos)
		if kind, _ := markerOf(line, size); kind == open {
			return true
		}
		pos = next
	}
	return false
}

// readSides reads the stages of path in the index.
func readSides(ctx context.Context, r *run.Runner, gitDir, workTree, path string) (Sides, error) {
	var s Sides
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, WorkTree: workTree, Read: true,
		Env:  []string{"GIT_LITERAL_PATHSPECS=1"},
		Args: []string{"ls-files", "-u", "-z", "--", path}})
	if err != nil {
		return s, err
	}
	for rec := range bytes.SplitSeq(res.Stdout, []byte{0}) {
		// mode SP id SP stage TAB path
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		f := bytes.Fields(meta)
		if !ok || string(name) != path || len(f) != 3 {
			continue
		}
		var side *[]byte
		switch string(f[2]) {
		case "1":
			side = &s.Base
		case "2":
			side = &s.Ours
		case "3":
			side = &s.Theirs
		default:
			continue
		}
		blob, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, MaxOutput: MaxSize,
			Args: []string{"cat-file", "blob", string(f[1])}})
		if errors.Is(err, run.ErrOutputTooLarge) {
			return s, errLarge
		}
		if err != nil {
			return s, err
		}
		*side = blob.Stdout
		if *side == nil {
			*side = []byte{}
		}
	}
	return s, nil
}
