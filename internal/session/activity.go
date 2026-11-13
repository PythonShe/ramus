package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"ramus/internal/git/activity"
	"ramus/internal/git/catfile"
	"ramus/internal/git/mailmap"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/stats"
)

// ActivitySource is what a repository's activity records are read from:
// the commits reachable from every branch, remote branch and tag, and the
// mailmap their authors go through.
type ActivitySource struct {
	// Key changes whenever the records would: it digests the tips and
	// the mailmap, so records read before under the same key still hold.
	Key  string
	tips [][]byte
	mm   *mailmap.Map
}

// FindActivity finds what a repository's activity records are read from,
// without reading a commit. It runs off the main thread.
func FindActivity(ctx context.Context, r *repo.Repository) (ActivitySource, error) {
	cat, err := r.CatFile()
	if err != nil {
		return ActivitySource{}, err
	}
	var tips [][]byte
	if rd := r.Refs(); rd != nil {
		head, err := rd.Head()
		if err != nil {
			return ActivitySource{}, err
		}
		list, err := rd.List()
		if err != nil {
			return ActivitySource{}, err
		}
		if tips, _, err = historyTips(cat, list, head); err != nil {
			return ActivitySource{}, err
		}
	} else if tips, err = gitTips(ctx, r); err != nil {
		return ActivitySource{}, err
	}
	data := mailmapData(ctx, r)
	var mm mailmap.Map
	h := sha256.New()
	slices.SortFunc(tips, bytes.Compare)
	for _, t := range tips {
		h.Write(t)
	}
	for _, d := range data {
		mm.Add(d)
		h.Write([]byte("\x00mailmap " + strconv.Itoa(len(d)) + "\x00"))
		h.Write(d)
	}
	return ActivitySource{Key: hex.EncodeToString(h.Sum(nil)), tips: tips, mm: &mm}, nil
}

// gitTips asks git for the tips when the engine does not read the refs:
// HEAD, and the branches, remote branches and tags, peeled.
func gitTips(ctx context.Context, r *repo.Repository) ([][]byte, error) {
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, MaxOutput: 64 << 20,
		Args: []string{"for-each-ref", "--format=%(objectname) %(*objectname)", "refs/heads/", "refs/remotes/", "refs/tags/"}})
	if err != nil {
		return nil, err
	}
	out := res.Stdout
	if h, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true,
		Args: []string{"rev-parse", "--verify", "--quiet", "HEAD"}}); err == nil {
		out = append(out, h.Stdout...)
	}
	seen := map[string]bool{}
	var tips [][]byte
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		id, err := hex.DecodeString(f[len(f)-1])
		if err == nil && !seen[string(id)] {
			seen[string(id)] = true
			tips = append(tips, id)
		}
	}
	return tips, nil
}

// activityReaders bounds the cat-file processes one repository's read
// runs.
const activityReaders = 4

// ReadActivity reads every commit src reaches into records. progress,
// when set, gets how many are read of how many, from a reading goroutine.
func ReadActivity(ctx context.Context, r *repo.Repository, src ActivitySource, progress func(read, total int)) (*activity.Records, error) {
	cat, err := r.CatFile()
	if err != nil {
		return nil, err
	}
	ids, err := statsCommits(ctx, r, cat, src.tips, StatsRequest{})
	if err != nil {
		return nil, err
	}
	readers := []stats.Reader{cat}
	for range activityReaders - 1 {
		p, err := catfile.Start(ctx, r.Runner, r.GitDir)
		if err != nil {
			break
		}
		defer p.Close()
		readers = append(readers, p)
	}
	var report func(int)
	if progress != nil {
		report = func(n int) { progress(n, len(ids)) }
	}
	return activity.Read(ctx, readers, ids, src.mm, report)
}

// RepoSize returns the bytes a repository's objects take on disk, loose
// and packed, as git count-objects says.
func RepoSize(ctx context.Context, r *repo.Repository) (int64, error) {
	res, err := r.Runner.Run(ctx, &run.Cmd{GitDir: r.GitDir, Read: true, Args: []string{"count-objects", "-v"}})
	if err != nil {
		return 0, err
	}
	var kib int64
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok || k != "size" && k != "size-pack" && k != "size-garbage" {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			kib += n
		}
	}
	return kib << 10, nil
}
