package session

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"ramus/internal/git/mailmap"
	"ramus/internal/git/repo"
)

// people is the mailmap the history and a commit's detail show names
// through, as git log and git show do, read again when what it is read
// from changes.
type people struct {
	mu    sync.Mutex
	stamp string
	mm    *mailmap.Map
}

// mailmap is the repository's mailmap as it stands. It runs off the main
// thread.
func (s *Session) mailmap(ctx context.Context, r *repo.Repository) *mailmap.Map {
	p := &s.people
	stamp := mailmapStamp(r)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mm != nil && p.stamp == stamp {
		return p.mm
	}
	var mm mailmap.Map
	for _, d := range mailmapData(ctx, r) {
		mm.Add(d)
	}
	p.mm, p.stamp = &mm, stamp
	return p.mm
}

// mailmapStamp changes when the files a mailmap is read from, or the
// settings naming them, do; a blob named is read once.
func mailmapStamp(r *repo.Repository) string {
	var b strings.Builder
	stat := func(p string) {
		if fi, err := os.Stat(p); err == nil {
			b.WriteString(strconv.FormatInt(fi.Size(), 10) + "@" + strconv.FormatInt(fi.ModTime().UnixNano(), 10))
		}
		b.WriteByte(0)
	}
	if r.WorkTree != "" {
		stat(filepath.Join(r.WorkTree, ".mailmap"))
	}
	blob, _ := r.Config().Get("mailmap.blob")
	f, _ := r.Config().Get("mailmap.file")
	b.WriteString(blob + "\x00" + f + "\x00")
	if rest, ok := strings.CutPrefix(f, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			f = filepath.Join(home, rest)
		}
	}
	if f != "" {
		stat(f)
	}
	return b.String()
}
