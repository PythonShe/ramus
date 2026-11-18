package repo

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"time"

	"ramus/internal/git/run"
)

// untrackedTimeout bounds the walk UntrackedUnder has git make of the
// folders.
const untrackedTimeout = 30 * time.Second

// UntrackedFiles are the files git does not track under a folder.
type UntrackedFiles struct {
	// Paths are the first of them, in git's order, slash-separated from
	// the top of the work tree.
	Paths []string
	// More counts those past them.
	More int
}

// maxEachArgBytes bounds the folders one ls-files of UntrackedUnder
// names: it takes them as arguments, and Windows limits a command line
// to 32 K characters.
const maxEachArgBytes = 24 << 10

// UntrackedUnder lists the files git does not track under each of dirs,
// folders of the work tree as git status names them ("newdir/"), none
// inside another, leaving out what is ignored as status does, in as few
// runs of git as their names fit: the first each of a folder, all names
// at most, and how many more. A folder holding none is left out, as are
// those of a run that failed, whose error is returned with the others; a
// repository inside one is listed as a folder, as status lists it. Off
// the main thread.
func (r *Repository) UntrackedUnder(ctx context.Context, dirs []string, each, all int) (map[string]UntrackedFiles, error) {
	out := map[string]UntrackedFiles{}
	if r.WorkTree == "" || len(dirs) == 0 {
		return out, nil
	}
	sorted := slices.Clone(dirs)
	slices.Sort(sorted)
	l := &eachLines{each: each, left: all, out: out}
	var first error
	for rest := sorted; len(rest) > 0 && ctx.Err() == nil; {
		n, size := 0, 0
		for n < len(rest) && (n == 0 || size+len(rest[n])+1 <= maxEachArgBytes) {
			size += len(rest[n]) + 1
			n++
		}
		batch := rest[:n]
		rest = rest[n:]
		l.dirs, l.part = batch, l.part[:0]
		_, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, WorkTree: r.WorkTree, Read: true, Stdout: l,
			Timeout: untrackedTimeout, Env: []string{"GIT_LITERAL_PATHSPECS=1"},
			Args: append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, batch...)})
		if err != nil {
			for _, d := range batch {
				delete(out, d)
			}
			first = cmp.Or(first, err)
		}
	}
	if first == nil {
		first = ctx.Err()
	}
	return out, first
}

// eachLines sorts ls-files -z output into the folders of dirs, sorted,
// as it comes: the first each names of a folder while left of all names
// remain, and a count of the others.
type eachLines struct {
	dirs       []string
	each, left int
	out        map[string]UntrackedFiles
	part       []byte
}

func (l *eachLines) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			l.part = append(l.part, b...)
			break
		}
		name := b[:i]
		if len(l.part) > 0 {
			name = append(l.part, name...)
		}
		l.add(name)
		l.part = l.part[:0]
		b = b[i+1:]
	}
	return n, nil
}

// add files name under the folder that holds it: the last of dirs not
// after it, which none other between holds.
func (l *eachLines) add(name []byte) {
	k, found := slices.BinarySearchFunc(l.dirs, name, func(d string, n []byte) int { return bytes.Compare([]byte(d), n) })
	if found {
		k++ // the folder itself: a repository inside the work tree
	}
	if k == 0 {
		return
	}
	dir := l.dirs[k-1]
	if !bytes.HasPrefix(name, []byte(dir)) {
		return
	}
	f := l.out[dir]
	if len(f.Paths) < l.each && l.left > 0 {
		f.Paths = append(f.Paths, string(name))
		l.left--
	} else {
		f.More++
	}
	l.out[dir] = f
}
