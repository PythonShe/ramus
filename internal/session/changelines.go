package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"ramus/internal/git/cache"
	"ramus/internal/git/diff"
	"ramus/internal/git/repo"
	"ramus/internal/git/status"
)

// ChangeLines are the lines each entry of a status changes, in its
// order: Staged in the index against HEAD, Unstaged in the work tree
// against the index, or, for a file git does not track, the whole file.
// An entry not counted has the zero diff.Lines.
type ChangeLines struct {
	For              *status.Status
	Staged, Unstaged []diff.Lines
	// order holds For's entries sorted by path, for Find.
	order []int32
}

// Find returns the counts of the entry at path, for a status read since
// For: the counts of that one come a little after it.
func (l *ChangeLines) Find(path []byte) (staged, unstaged diff.Lines) {
	k, ok := slices.BinarySearchFunc(l.order, path, func(e int32, p []byte) int { return bytes.Compare(l.For.Path(int(e)), p) })
	if !ok {
		return diff.Lines{}, diff.Lines{}
	}
	return l.Staged[l.order[k]], l.Unstaged[l.order[k]]
}

// maxCountedFiles bounds the files git does not track that the app
// reads to count, as maxCountedFile and maxCountedBytes bound what of
// them it reads: past them a file is not counted.
const maxCountedFiles = 10000

// countBudget bounds the counts kept by what they were counted from.
const countBudget = 4 << 20

// countRecent is how new a file's modification time may be for its count
// to be kept: one changed again within the clock's tick, at the same
// size, would look the same.
const countRecent = 2 * time.Second

// countSlot runs the counts of the changes' lines one at a time. A count
// asked for while one runs waits for it, the newest only: under edits
// that never stop, a slow count cancelled by each new status would never
// end. Main thread only.
type countSlot struct {
	running bool
	next    *countAsk
	// index is the key of the index the status statusFor was read with,
	// indexOK set when it is known: counts keyed on the index's objects
	// the status names are kept only while the index is that one.
	statusFor *status.Status
	index     repo.IndexKey
	indexOK   bool
	// kept holds counts by what they were counted from, for r. Only the
	// running count reads and writes it: counts run one at a time, and
	// each starts from the main thread after the last has ended.
	r    *repo.Repository
	kept *cache.Cache[string, keptLines]
}

type countAsk struct {
	r    *repo.Repository
	ctx  context.Context
	st   *status.Status
	done func(ChangeLines)
	// index is the key of the index st was read with, when indexOK.
	index   repo.IndexKey
	indexOK bool
}

type keptLines struct {
	lines diff.Lines
	bytes int
}

// CountChanges counts the lines of the changes st lists off the main
// thread and hands them to done on it. One asked for while another runs
// starts once it ended, unless a newer one is asked for first, which
// takes its place, whose done is then not called; a count whose view was
// left is not handed over.
func (s *Session) CountChanges(st *status.Status, done func(ChangeLines)) {
	r := s.Repo
	if r == nil || st == nil {
		return
	}
	a := &countAsk{r: r, ctx: s.viewContext(r), st: st, done: done}
	if c := &s.count; c.statusFor == st {
		a.index, a.indexOK = c.index, c.indexOK
	}
	// One running for a repository since closed, whose end never came,
	// holds up none.
	if s.count.running && s.count.r == r {
		s.count.next = a
		return
	}
	s.startCount(a)
}

func (s *Session) startCount(a *countAsk) {
	c := &s.count
	if c.r != a.r {
		c.r, c.kept = a.r, cache.New[string](countBudget, func(k keptLines) int { return k.bytes })
	}
	kept := c.kept
	c.running = true
	err := a.r.Life().Go(func(context.Context) {
		var out ChangeLines
		ok := false
		// Deferred, to run though the count panicked: else no count
		// would run again.
		defer s.update(func() {
			c.running = false
			if ok && a.ctx.Err() == nil {
				a.done(out)
			}
			if next := c.next; next != nil {
				c.next = nil
				if next.ctx.Err() == nil && next.r == s.Repo {
					s.startCount(next)
				}
			}
		})
		out, ok = countChanged(a.ctx, a.r, a.st, a.index, a.indexOK, kept)
	})
	if err != nil {
		c.running = false
	}
}

// countChanged counts the lines of st's changes, taking those counted
// before from kept where what they were counted from is the same: the
// objects for a staged change, and the work-tree file's size and
// modification time besides for the rest.
func countChanged(ctx context.Context, r *repo.Repository, st *status.Status, index repo.IndexKey, indexOK bool, kept *cache.Cache[string, keptLines]) (ChangeLines, bool) {
	// Counts of changes keyed on the index's objects as st names them
	// are kept only when git counted from that index: one written since
	// st was read would have them kept under objects they are not of.
	sameIndex := func() bool {
		k, err := repo.ReadIndexKey(r.GitDir)
		return indexOK && err == nil && k == index
	}
	keepIndexed := sameIndex()
	out := ChangeLines{For: st, Staged: make([]diff.Lines, st.Len()), Unstaged: make([]diff.Lines, st.Len())}
	type ask struct {
		i   int
		key string // "" when not to keep
	}
	var staged, unstaged []string
	var stagedAsk, unstagedAsk []ask
	var untracked []int
	renames := false
	now := time.Now()
	for i := range st.Len() {
		if i&4095 == 0 && ctx.Err() != nil {
			return out, false
		}
		k := st.Kind(i)
		path := st.Path(i)
		switch k {
		case status.Untracked:
			if !bytes.HasSuffix(path, []byte("/")) {
				untracked = append(untracked, i)
			}
			continue
		case status.Changed, status.Renamed:
		default:
			continue
		}
		x, y := st.XY(i)
		oids := st.OIDs(i)
		if x != '.' {
			if k == status.Renamed && x == 'R' {
				staged = append(staged, string(path), string(st.OrigPath(i)))
				stagedAsk = append(stagedAsk, ask{i: i})
				renames = true
			} else if key := objectsKey(path, oids[0], oids[1]); !take(kept, key, &out.Staged[i]) {
				staged = append(staged, string(path))
				stagedAsk = append(stagedAsk, ask{i, key})
			}
		}
		if y != '.' {
			if key := stampKey('u', r.WorkTree, path, oids[1], now); !take(kept, key, &out.Unstaged[i]) {
				unstaged = append(unstaged, string(path))
				unstagedAsk = append(unstagedAsk, ask{i, key})
			}
		}
	}
	base := st.Branch.OID
	if base == "" {
		base = hex.EncodeToString(emptyTree(r.Caps.HashSize))
	}
	var inIndex, inTree *diff.NumStat
	var err error
	if len(staged) > 0 {
		if inIndex, err = diff.WorkTreeLines(ctx, r.Runner, r.GitDir, r.WorkTree, true, base, renames, staged); err != nil {
			inIndex = nil
		}
	}
	if len(unstaged) > 0 && ctx.Err() == nil {
		if inTree, err = diff.WorkTreeLines(ctx, r.Runner, r.GitDir, r.WorkTree, false, "", false, unstaged); err != nil {
			inTree = nil
		}
	}
	files, read := 0, 0
	for _, i := range untracked {
		if ctx.Err() != nil || files >= maxCountedFiles || read >= maxCountedBytes {
			break
		}
		path := st.Path(i)
		key := stampKey('n', r.WorkTree, path, nil, now)
		if take(kept, key, &out.Unstaged[i]) {
			continue
		}
		b, err := readWorkTreeFile(r.WorkTree, string(path), min(maxCountedFile, maxCountedBytes-read))
		if err != nil {
			continue
		}
		files++
		read += len(b)
		out.Unstaged[i] = diff.CountText(b)
		keep(kept, key, out.Unstaged[i])
	}
	if ctx.Err() != nil {
		return out, false
	}
	keepIndexed = keepIndexed && sameIndex()
	for _, a := range stagedAsk {
		path := st.Path(a.i)
		old := path
		if x, _ := st.XY(a.i); st.Kind(a.i) == status.Renamed && x == 'R' {
			old = st.OrigPath(a.i)
		}
		out.Staged[a.i] = inIndex.Find(old, path)
		if keepIndexed {
			keep(kept, a.key, out.Staged[a.i])
		}
	}
	for _, a := range unstagedAsk {
		path := st.Path(a.i)
		out.Unstaged[a.i] = inTree.Find(path, path)
		if keepIndexed {
			keep(kept, a.key, out.Unstaged[a.i])
		}
	}
	out.order = make([]int32, st.Len())
	for i := range out.order {
		out.order[i] = int32(i)
	}
	slices.SortFunc(out.order, func(a, b int32) int { return bytes.Compare(st.Path(int(a)), st.Path(int(b))) })
	return out, true
}

// objectsKey is what a staged change is counted from: the path and its
// objects in HEAD and the index.
func objectsKey(path, head, index []byte) string {
	return string(slices.Concat([]byte{'s'}, path, []byte{0}, head, index))
}

// stampKey is what an unstaged change (kind 'u', with the index's object)
// or a file git does not track (kind 'n') is counted from: the work-tree
// file's size and modification time, or that it is gone. It is "" for
// what is not to be kept: not a file, or one changed too lately to tell
// from a change to come.
func stampKey(kind byte, workTree string, path, index []byte, now time.Time) string {
	fi, err := os.Lstat(filepath.Join(workTree, filepath.FromSlash(string(path))))
	var stamp [17]byte
	switch {
	case errors.Is(err, fs.ErrNotExist):
		stamp[16] = 1 // gone: what was in the index is counted deleted
	case err != nil, !fi.Mode().IsRegular(), now.Sub(fi.ModTime()) < countRecent:
		return ""
	default:
		binary.LittleEndian.PutUint64(stamp[:8], uint64(fi.Size()))
		binary.LittleEndian.PutUint64(stamp[8:16], uint64(fi.ModTime().UnixNano()))
	}
	return string(slices.Concat([]byte{kind}, path, []byte{0}, index, stamp[:]))
}

// take sets *l to the count kept for key, if any.
func take(kept *cache.Cache[string, keptLines], key string, l *diff.Lines) bool {
	if key == "" {
		return false
	}
	k, ok := kept.Get(key)
	if ok {
		*l = k.lines
	}
	return ok
}

// keep keeps l for key, once counted.
func keep(kept *cache.Cache[string, keptLines], key string, l diff.Lines) {
	if key != "" && l.Known {
		kept.Put(key, keptLines{l, len(key) + 64})
	}
}
