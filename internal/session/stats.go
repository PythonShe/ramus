package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/commitgraph"
	"ramus/internal/git/commits"
	"ramus/internal/git/history"
	"ramus/internal/git/mailmap"
	"ramus/internal/git/pathsafe"
	"ramus/internal/git/refs"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/stats"
)

// StatsRequest names the commits counted: those reachable from Branch (a
// full ref name), or from every branch, remote branch and tag when it is
// empty, with a commit time in [Since, Until); a zero bound is open.
type StatsRequest struct {
	Branch       string
	Since, Until time.Time
}

// StatsResult is the counts so far; Done once every commit is read.
type StatsResult struct {
	Req         StatsRequest
	Stats       stats.Stats
	Read, Total int
	Done        bool
	Err         error
}

// statsLoad cancels a count a newer request replaced.
type statsLoad struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	gen    int
}

// statsReaders bounds the cat-file processes one count runs.
const statsReaders = 4

// LoadStats counts commits off the main thread, handing the counts to
// publish on it as they grow. A newer call cancels one still running,
// whose publish is not called again. A count whose repository closed and
// opened again (Trust) ends with ErrReopened.
func (s *Session) LoadStats(req StatsRequest, publish func(StatsResult)) {
	r := s.Repo
	if r == nil || s.State.Refs == nil {
		return
	}
	list, head := s.State.Refs, s.State.Head
	s.stats.mu.Lock()
	if s.stats.cancel != nil {
		s.stats.cancel()
	}
	ctx, cancel := context.WithCancel(s.viewContext(r))
	s.stats.gen++
	gen := s.stats.gen
	s.stats.cancel = cancel
	s.stats.mu.Unlock()
	latest := func() bool {
		s.stats.mu.Lock()
		defer s.stats.mu.Unlock()
		return s.stats.gen == gen
	}
	shown := s.stillShown()
	reopened := func() {
		if latest() && shown() {
			publish(StatsResult{Req: req, Done: true, Err: ErrReopened})
		}
	}
	send := func(res StatsResult, end bool) {
		res.Req = req
		var after func()
		if end {
			after = reopened
		}
		s.handOver(r, func() {
			if latest() {
				publish(res)
			}
		}, after)
	}
	r.Life().Go(func(context.Context) {
		defer cancel()
		res, err := countStats(ctx, r, req, list, head, func(read, total int, st stats.Stats) {
			send(StatsResult{Stats: st, Read: read, Total: total}, false)
		})
		if ctx.Err() != nil {
			if closedUnder(r) {
				s.handOver(r, nil, reopened)
			}
			return
		}
		res.Done, res.Err = true, err
		send(res, true)
	})
}

func countStats(ctx context.Context, r *repo.Repository, req StatsRequest, list *refs.List, head refs.Head, progress func(read, total int, st stats.Stats)) (StatsResult, error) {
	cat, err := r.CatFile()
	if err != nil {
		return StatsResult{}, err
	}
	var tips [][]byte
	if req.Branch == "" {
		if tips, _, err = historyTips(ctx, cat, list, head, HistoryView{}); err != nil {
			return StatsResult{}, err
		}
	} else {
		for i := range list.Len() {
			if list.Name(i) == req.Branch {
				id := list.Peeled(i)
				if id == nil {
					id = list.OID(i)
				}
				tips = [][]byte{id}
			}
		}
		if tips == nil {
			return StatsResult{}, errors.New("session: there is no " + req.Branch)
		}
	}
	ids, err := statsCommits(ctx, r, cat, tips, req)
	if err != nil {
		return StatsResult{}, err
	}
	mm := loadMailmap(ctx, r)
	more, stop := extraReaders(ctx, r, statsReaders-1)
	defer stop()
	st, err := stats.Collect(ctx, append([]stats.Reader{cat}, more...), ids, mm, func(read int, st stats.Stats) { progress(read, ids.Len(), st) })
	return StatsResult{Stats: st, Read: ids.Len(), Total: ids.Len()}, err
}

// readerSlots bounds, app-wide, the cat-file processes started beside a
// repository's own for a long read. Those last the whole read and take no
// slot of the runner's pool, so reads in several repositories at once
// would start any number.
var readerSlots = make(chan struct{}, 4)

// extraReaders starts up to n cat-file processes to read with, as many as
// readerSlots has free: a read with fewer is slower, not wrong. stop
// ends them.
func extraReaders(ctx context.Context, r *repo.Repository, n int) (readers []stats.Reader, stop func()) {
	var ps []*catfile.Process
start:
	for range n {
		select {
		case readerSlots <- struct{}{}:
		default:
			break start
		}
		p, err := catfile.Start(ctx, r.Runner, r.GitDir)
		if err != nil {
			<-readerSlots
			break
		}
		ps = append(ps, p)
		readers = append(readers, p)
	}
	return readers, func() {
		for _, p := range ps {
			p.Close()
			<-readerSlots
		}
	}
}

// statsCommits lists the commits reachable from tips with a commit time
// in the requested range: from the commit-graph without reading a commit
// when it holds them, else from git rev-list.
func statsCommits(ctx context.Context, r *repo.Repository, cat *catfile.Process, tips [][]byte, req StatsRequest) (stats.IDs, error) {
	in := func(t int64) bool {
		return (req.Since.IsZero() || t >= req.Since.Unix()) && (req.Until.IsZero() || t < req.Until.Unix())
	}
	g, err := r.CommitGraph()
	if err != nil {
		return stats.IDs{}, err
	}
	if g != nil {
		ts, err := commitTips(ctx, cat, g, tips)
		if err != nil {
			return stats.IDs{}, err
		}
		if pos, ok := history.Covered(g, ts); ok {
			return graphCommits(ctx, g, pos, in)
		}
	}
	store := commits.New(r.Caps.HashSize)
	var ids []uint32
	err = history.RevList(ctx, r.Runner, r.GitDir, tips, store, func(id uint32, _ []uint32) error {
		if in(store.CommitTime(id)) {
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return stats.IDs{}, err
	}
	snap := store.Snapshot()
	out := stats.IDs{Flat: make([]byte, 0, len(ids)*r.Caps.HashSize), Width: r.Caps.HashSize}
	for _, id := range ids {
		out.Append(snap.OID(id))
	}
	return out, nil
}

func graphCommits(ctx context.Context, g *commitgraph.Graph, pos []uint32, in func(int64) bool) (stats.IDs, error) {
	var out stats.IDs
	w := history.NewWalk(g, pos)
	for n := 0; ; n++ {
		p, _, ok := w.Next()
		if !ok {
			break
		}
		if n&65535 == 0 && ctx.Err() != nil {
			return stats.IDs{}, ctx.Err()
		}
		if in(g.CommitTime(p)) {
			// Copied into one buffer: the graph's mapping goes once
			// nothing holds g, and one allocation per commit would be a
			// history's worth of objects.
			out.Append(g.OID(p))
		}
	}
	return out, w.Err()
}

// maxMailmap bounds a mailmap file read.
const maxMailmap = 4 << 20

// loadMailmap reads the mailmaps git would.
func loadMailmap(ctx context.Context, r *repo.Repository) *mailmap.Map {
	var mm mailmap.Map
	for _, d := range mailmapData(ctx, r) {
		mm.Add(d)
	}
	return &mm
}

// mailmapData returns the mailmap files git would read, in its order:
// .mailmap in the work tree (a symlink not followed), mailmap.blob
// (HEAD:.mailmap in a bare repository) and mailmap.file. One that cannot
// be read is left out, as names are not worth an error.
func mailmapData(ctx context.Context, r *repo.Repository) [][]byte {
	var out [][]byte
	readFile := func(p string, follow bool) {
		stat := os.Lstat
		if follow {
			stat = os.Stat
		}
		fi, err := stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxMailmap {
			return
		}
		// The config names the file: regular still when read, not a
		// named pipe put there since.
		if data, err := pathsafe.ReadFile(p, maxMailmap); err == nil {
			out = append(out, data)
		}
	}
	if r.WorkTree != "" {
		readFile(filepath.Join(r.WorkTree, ".mailmap"), false)
	}
	blob, ok := r.Config().Get("mailmap.blob")
	if !ok && r.WorkTree == "" {
		blob = "HEAD:.mailmap"
	}
	if blob != "" {
		res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, MaxOutput: maxMailmap,
			Args: []string{"cat-file", "blob", "--end-of-options", blob}})
		if err == nil {
			out = append(out, res.Stdout)
		}
	}
	if f, ok := r.Config().Get("mailmap.file"); ok && f != "" {
		if rest, ok := strings.CutPrefix(f, "~/"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				f = filepath.Join(home, rest)
			}
		}
		readFile(f, true)
	}
	return out
}
