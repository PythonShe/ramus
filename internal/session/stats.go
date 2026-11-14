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
// whose publish is not called again.
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
	send := func(res StatsResult) {
		res.Req = req
		s.update(func() {
			s.stats.mu.Lock()
			ok := s.stats.gen == gen
			s.stats.mu.Unlock()
			if ok {
				publish(res)
			}
		})
	}
	r.Life().Go(func(context.Context) {
		defer cancel()
		res, err := countStats(ctx, r, req, list, head, func(read, total int, st stats.Stats) {
			send(StatsResult{Stats: st, Read: read, Total: total})
		})
		if ctx.Err() != nil {
			return
		}
		res.Done, res.Err = true, err
		send(res)
	})
}

func countStats(ctx context.Context, r *repo.Repository, req StatsRequest, list *refs.List, head refs.Head, progress func(read, total int, st stats.Stats)) (StatsResult, error) {
	cat, err := r.CatFile()
	if err != nil {
		return StatsResult{}, err
	}
	var tips [][]byte
	if req.Branch == "" {
		if tips, _, err = historyTips(cat, list, head, HistoryView{}); err != nil {
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
	readers := []stats.Reader{cat}
	for range statsReaders - 1 {
		p, err := catfile.Start(ctx, r.Runner, r.GitDir)
		if err != nil {
			break
		}
		defer p.Close()
		readers = append(readers, p)
	}
	st, err := stats.Collect(ctx, readers, ids, mm, func(read int, st stats.Stats) { progress(read, len(ids), st) })
	return StatsResult{Stats: st, Read: len(ids), Total: len(ids)}, err
}

// statsCommits lists the commits reachable from tips with a commit time
// in the requested range: from the commit-graph without reading a commit
// when it holds them, else from git rev-list.
func statsCommits(ctx context.Context, r *repo.Repository, cat *catfile.Process, tips [][]byte, req StatsRequest) ([][]byte, error) {
	in := func(t int64) bool {
		return (req.Since.IsZero() || t >= req.Since.Unix()) && (req.Until.IsZero() || t < req.Until.Unix())
	}
	g, err := r.CommitGraph()
	if err != nil {
		return nil, err
	}
	if g != nil {
		ts, err := commitTips(cat, g, tips)
		if err != nil {
			return nil, err
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
		return nil, err
	}
	snap := store.Snapshot()
	out := make([][]byte, len(ids))
	for i, id := range ids {
		out[i] = snap.OID(id)
	}
	return out, nil
}

func graphCommits(ctx context.Context, g *commitgraph.Graph, pos []uint32, in func(int64) bool) ([][]byte, error) {
	var out [][]byte
	w := history.NewWalk(g, pos)
	for n := 0; ; n++ {
		p, _, ok := w.Next()
		if !ok {
			break
		}
		if n&65535 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if in(g.CommitTime(p)) {
			out = append(out, g.OID(p))
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
		if data, err := os.ReadFile(p); err == nil {
			out = append(out, data)
		}
	}
	if r.WorkTree != "" {
		readFile(filepath.Join(r.WorkTree, ".mailmap"), false)
	}
	blob, ok := r.Config.Get("mailmap.blob")
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
	if f, ok := r.Config.Get("mailmap.file"); ok && f != "" {
		if rest, ok := strings.CutPrefix(f, "~/"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				f = filepath.Join(home, rest)
			}
		}
		readFile(f, true)
	}
	return out
}
