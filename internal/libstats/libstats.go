// Package libstats reads statistics across the repositories in the
// library without opening a tab: each repository's commits as activity
// records, cached in memory and on disk under a key of its refs and
// mailmap, so a repository is read again only once it has changed.
package libstats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"ramus/internal/git/activity"
	"ramus/internal/git/cache"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/session"
)

// Repo is one repository's part of the statistics, as far as it is read.
type Repo struct {
	Path, Name string
	// Records are its commits once read; nil before.
	Records *activity.Records
	// Size is the bytes its objects take on disk.
	Size int64
	// Email is the repository's user.email.
	Email string
	// Read of Total commits are read while it is being read.
	Read, Total int
	Done        bool
	Err         error
}

// Budgets bound the records kept: in memory, and in the cache directory.
const (
	MemoryBudget = 256 << 20
	DiskBudget   = 512 << 20
)

// openTimeout bounds opening one repository.
const openTimeout = time.Minute

// progressEvery bounds how often a read in progress is published.
const progressEvery = 100 * time.Millisecond

// Loader reads repositories one at a time off the main thread. Its
// methods are called on the main thread.
type Loader struct {
	runner  *run.Runner
	trusted func(commonDir string) bool
	dir     string
	update  func(func())

	mu     sync.Mutex
	mem    *cache.Cache[string, entry] // by path; the cache is not safe for concurrent use
	cancel context.CancelFunc
	gen    int
	ctx    context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
}

type entry struct {
	key string
	rec *activity.Records
}

// New returns a loader running git through runner; trusted says whether
// the user trusts a repository by its common git directory, one not
// trusted read through a hardened git. dir, when set, keeps records
// across launches. update runs a function on the main thread.
func New(runner *run.Runner, trusted func(string) bool, dir string, update func(func())) *Loader {
	ctx, stop := context.WithCancel(context.Background())
	return &Loader{
		runner: runner, trusted: trusted, dir: dir, update: update, ctx: ctx, stop: stop,
		mem: cache.New[string, entry](MemoryBudget, func(e entry) int { return e.rec.Bytes() }),
	}
}

// Load reads the repositories at paths, named names, in order, handing
// each one's state to publish on the main thread as it grows. A newer
// call, or Cancel, stops it; its publish is not called again.
func (l *Loader) Load(paths, names []string, publish func(i int, r Repo)) {
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel()
	}
	ctx, cancel := context.WithCancel(l.ctx)
	l.gen++
	gen := l.gen
	l.cancel = cancel
	l.mu.Unlock()
	send := func(i int, r Repo) {
		l.update(func() {
			l.mu.Lock()
			ok := l.gen == gen
			l.mu.Unlock()
			if ok {
				publish(i, r)
			}
		})
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer cancel()
		for i, p := range paths {
			if ctx.Err() != nil {
				return
			}
			r := Repo{Path: p, Name: names[i]}
			var last time.Time
			err := l.read(ctx, &r, func(read, total int) {
				if now := time.Now(); now.Sub(last) >= progressEvery {
					last = now
					send(i, Repo{Path: r.Path, Name: r.Name, Read: read, Total: total})
				}
			})
			if ctx.Err() != nil {
				return
			}
			r.Done, r.Err = true, err
			send(i, r)
		}
	}()
}

// Cancel stops what Load started.
func (l *Loader) Cancel() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
	}
	l.gen++
}

// Close stops the loader and waits until its repositories are closed.
func (l *Loader) Close() {
	l.stop()
	l.wg.Wait()
}

func (l *Loader) read(ctx context.Context, out *Repo, progress func(read, total int)) error {
	octx, cancel := context.WithTimeout(ctx, openTimeout)
	r, err := repo.Open(octx, l.runner, out.Path, func(loc repo.Location) bool {
		return l.trusted != nil && l.trusted(loc.CommonDir)
	})
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		r.Close()
		<-r.Life().Done()
	}()
	out.Email, _ = r.Config().Get("user.email")
	if out.Size, err = session.RepoSize(ctx, r); err != nil {
		return err
	}
	src, err := session.FindActivity(ctx, r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	e, ok := l.mem.Get(out.Path)
	l.mu.Unlock()
	if ok && e.key == src.Key {
		out.Records = e.rec
		return nil
	}
	if rec := l.fromDisk(out.Path, src.Key); rec != nil {
		out.Records = rec
		l.keep(out.Path, src.Key, rec)
		return nil
	}
	rec, err := session.ReadActivity(ctx, r, src, progress)
	if err != nil {
		return err
	}
	out.Records = rec
	l.keep(out.Path, src.Key, rec)
	l.toDisk(out.Path, src.Key, rec)
	return nil
}

func (l *Loader) keep(path, key string, rec *activity.Records) {
	l.mu.Lock()
	l.mem.Put(path, entry{key, rec})
	l.mu.Unlock()
}

// file is where a repository's records are kept on disk.
func (l *Loader) file(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(l.dir, hex.EncodeToString(sum[:12])+".activity")
}

func (l *Loader) fromDisk(path, key string) *activity.Records {
	if l.dir == "" {
		return nil
	}
	f, err := os.Open(l.file(path))
	if err != nil {
		return nil
	}
	defer f.Close()
	rec, k, err := activity.Decode(f)
	if err != nil || k != key {
		return nil
	}
	// Used again: the budget drops the longest unused first.
	now := time.Now()
	os.Chtimes(f.Name(), now, now)
	return rec
}

// toDisk writes the records whole, then drops the longest unused files
// past the budget. A cache that cannot be written is only slower.
func (l *Loader) toDisk(path, key string, rec *activity.Records) {
	if l.dir == "" || os.MkdirAll(l.dir, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(l.dir, "write-*")
	if err != nil {
		return
	}
	err = rec.Encode(tmp, key)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), l.file(path))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return
	}
	trim(l.dir, DiskBudget)
}

// trim removes the least recently used record files in dir until they
// fit budget bytes.
func trim(dir string, budget int64) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		path string
		size int64
		at   time.Time
	}
	var files []file
	var total int64
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "write-") {
			// Left by a write that never finished.
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Hour {
				os.Remove(filepath.Join(dir, e.Name()))
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".activity") {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
			files = append(files, file{filepath.Join(dir, e.Name()), fi.Size(), fi.ModTime()})
			total += fi.Size()
		}
	}
	slices.SortFunc(files, func(a, b file) int { return a.at.Compare(b.at) })
	for _, f := range files {
		if total <= budget {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}
