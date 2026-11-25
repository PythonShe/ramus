package session

import (
	"context"

	"ramus/internal/git/repo"
	"ramus/internal/git/status"
)

// FolderCounts are how many files each untracked folder of a status
// holds: status lists such a folder whole, as one entry, and a count of
// the files changed counts what is in it.
type FolderCounts struct {
	// For is the status whose folders were counted.
	For *status.Status
	// Extra is how many files For's folders hold past the one entry each
	// of them is.
	Extra int
	n     map[string]int
}

// Files is how many files the untracked folder dir holds, as status
// names it ("new/"): one while it is not counted, as its entry is one.
func (c *FolderCounts) Files(dir []byte) int {
	if c == nil {
		return 1
	}
	if n := c.n[string(dir)]; n > 0 {
		return n
	}
	return 1
}

// over is c with Extra summed over dirs, the folders of a status other
// than the one counted: a folder gone since adds nothing, and one new
// is the one entry it is until counted.
func (c *FolderCounts) over(dirs []string) *FolderCounts {
	out := &FolderCounts{For: c.For, n: c.n}
	for _, d := range dirs {
		if n := c.n[d]; n > 0 {
			out.Extra += n - 1
		}
	}
	return out
}

// folderSlot counts the files of the untracked folders one status at a
// time. A status read while one is counted waits for it, the newest
// only, as a count of lines does (countSlot). Main thread only.
type folderSlot struct {
	running   bool
	cur, next *folderAsk
	// shown is the status in State, and dirs its untracked folders.
	shown *status.Status
	dirs  []string
}

type folderAsk struct {
	r    *repo.Repository
	st   *status.Status
	dirs []string
	// stale is set once a status without such folders was read: there
	// is then nothing its counts are of.
	stale bool
}

// untrackedFolders are the folders st lists whole. Any goroutine.
func untrackedFolders(st *status.Status) []string {
	var out []string
	for i := range st.Len() {
		if st.Kind(i) != status.Untracked {
			continue
		}
		if p := st.Path(i); len(p) > 0 && p[len(p)-1] == '/' {
			out = append(out, string(p))
		}
	}
	return out
}

// countFolders counts the files of dirs, the untracked folders of st,
// the status just read of r, into State.Folders. The counts of the
// status before stay there meanwhile: most statuses leave the folders
// as they were.
func (s *Session) countFolders(r *repo.Repository, st *status.Status, dirs []string) {
	c := &s.folders
	c.shown, c.dirs = st, dirs
	if len(dirs) == 0 {
		if c.cur != nil {
			c.cur.stale = true
		}
		c.next = nil
		s.State.Folders = nil
		return
	}
	if old := s.State.Folders; old != nil {
		s.State.Folders = old.over(dirs)
	}
	a := &folderAsk{r: r, st: st, dirs: dirs}
	// One running for a repository since closed, whose end never came,
	// holds up none.
	if c.running && c.cur.r == r {
		c.next = a
		return
	}
	s.startFolderCount(a)
}

func (s *Session) startFolderCount(a *folderAsk) {
	c := &s.folders
	c.running, c.cur = true, a
	ctx := a.r.Life().Context()
	err := a.r.Life().Go(func(context.Context) {
		var out *FolderCounts
		// Deferred, to run though the count panicked: else no count
		// would run again.
		defer s.update(func() {
			if c.cur != a {
				return
			}
			c.running, c.cur = false, nil
			if out != nil && !a.stale && s.Repo == a.r {
				if c.shown != a.st {
					// Of a status since replaced, whose own count is
					// still to run.
					out = out.over(c.dirs)
				}
				s.State.Folders = out
			}
			if next := c.next; next != nil {
				c.next = nil
				if next.r == s.Repo {
					s.startFolderCount(next)
				}
			}
		})
		// Counted, none named. A folder that could not be counted stays
		// the one entry it is.
		listed, _ := a.r.UntrackedUnder(ctx, a.dirs, 0, 0)
		if ctx.Err() != nil {
			return
		}
		fc := &FolderCounts{For: a.st, n: make(map[string]int, len(listed))}
		for dir, f := range listed {
			if n := len(f.Paths) + f.More; n > 0 {
				fc.n[dir] = n
				fc.Extra += n - 1
			}
		}
		out = fc
	})
	if err != nil {
		c.running, c.cur = false, nil
	}
}
