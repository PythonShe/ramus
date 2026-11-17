package session

import (
	"context"
	"path"
	"sync"
	"sync/atomic"

	"ramus/internal/git/repo"
	"ramus/internal/git/watch"
)

// perDirectory is watch.PerDirectory, for tests to take the path inotify
// takes where it is not.
var perDirectory = watch.PerDirectory

// ignoredDirs are the work tree's ignored directories (node_modules,
// target, build output), which the watcher leaves out where a watch costs
// one per directory: they can hold more directories than the system
// allows watches, and a build or an install writing to them would fill
// its queue. git ignores what changes there, so status misses nothing.
//
// They are read at open, before the watcher walks the work tree, and
// again when a .gitignore changes or the watcher lost notices (as an
// install filling its queue does): a directory no longer ignored is then
// watched again, and one ignored since is skipped when it is made anew.
// One made after the open, as a first build's output, keeps its watches
// until the repository opens again: that costs watches, never a change
// missed. Ignore rules outside the work tree (info/exclude,
// core.excludesFile) are not watched: a directory they stop ignoring is
// watched again only at the next read.
type ignoredDirs struct {
	set atomic.Pointer[map[string]bool]
	mu  sync.Mutex
	// reading is set while a read runs; again, when another was asked
	// for since it started.
	reading, again bool
}

// skip is the watcher's SkipDir: safe from any goroutine.
func (d *ignoredDirs) skip(rel string) bool {
	m := d.set.Load()
	return m != nil && (*m)[rel]
}

// replace makes dirs the set, and returns the directories skipped before
// that are not in it: they want watching again.
func (d *ignoredDirs) replace(dirs []string) (unskipped []string) {
	m := make(map[string]bool, len(dirs))
	for _, p := range dirs {
		m[p] = true
	}
	if old := d.set.Swap(&m); old != nil {
		for p := range *old {
			if !m[p] {
				unskipped = append(unskipped, p)
			}
		}
	}
	return unskipped
}

// read reads the set for r, and watches again what it no longer holds.
// Off the main thread.
func (d *ignoredDirs) read(ctx context.Context, r *repo.Repository) {
	dirs, err := r.IgnoredDirs(ctx)
	if err != nil {
		return // the set as it was skips no more than it did
	}
	r.Rewatch(d.replace(dirs))
}

// reread reads the set again in the background: once more after the
// read that runs, if one does.
func (d *ignoredDirs) reread(r *repo.Repository) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reading {
		d.again = true
		return
	}
	d.reading = true
	err := r.Life().Go(func(ctx context.Context) {
		for {
			d.read(ctx, r)
			d.mu.Lock()
			again := d.again && ctx.Err() == nil
			d.again, d.reading = false, again
			d.mu.Unlock()
			if !again {
				return
			}
		}
	})
	if err != nil {
		d.reading = false
	}
}

// list is the directories skipped now.
func (d *ignoredDirs) list() []string {
	m := d.set.Load()
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(*m))
	for p := range *m {
		out = append(out, p)
	}
	return out
}

// indexChanged reads the set again, in the background, when the index
// now has a file in a directory skipped: one added there with git add
// -f, whose changes the watcher must see.
func (d *ignoredDirs) indexChanged(r *repo.Repository) {
	dirs := d.list()
	if len(dirs) == 0 {
		return
	}
	r.Life().Go(func(ctx context.Context) {
		if r.TracksUnder(ctx, dirs) && ctx.Err() == nil {
			d.reread(r)
		}
	})
}

// changesIgnores reports whether a notice may change which directories
// are ignored.
func changesIgnores(n repo.Notice) bool {
	if n.What&repo.Rescan != 0 {
		return true
	}
	for _, p := range n.Paths {
		if path.Base(p) == ".gitignore" {
			return true
		}
	}
	return false
}
