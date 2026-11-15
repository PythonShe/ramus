package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"slices"
	"strings"

	"ramus/internal/git/diff"
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

// CountChanges counts the lines of the changes st lists off the main
// thread and hands them to done on it. A newer call cancels a count still
// running, whose done is not called.
func (s *Session) CountChanges(st *status.Status, done func(ChangeLines)) {
	r := s.Repo
	if r == nil || st == nil {
		return
	}
	slot := &s.changeLines
	slot.mu.Lock()
	if slot.cancel != nil {
		slot.cancel()
	}
	ctx, cancel := context.WithCancel(s.viewContext(r))
	slot.cancel = cancel
	slot.mu.Unlock()
	var staged, unstaged, untracked []string
	renames := false
	for i := range st.Len() {
		k := st.Kind(i)
		path := string(st.Path(i))
		switch k {
		case status.Untracked:
			if !strings.HasSuffix(path, "/") {
				untracked = append(untracked, path)
			}
			continue
		case status.Changed, status.Renamed:
		default:
			continue
		}
		x, y := st.XY(i)
		if x != '.' {
			staged = append(staged, path)
			if k == status.Renamed && x == 'R' {
				staged = append(staged, string(st.OrigPath(i)))
				renames = true
			}
		}
		if y != '.' {
			unstaged = append(unstaged, path)
		}
	}
	base := st.Branch.OID
	if base == "" {
		base = hex.EncodeToString(emptyTree(r.Caps.HashSize))
	}
	r.Life().Go(func(context.Context) {
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
		own := map[string]diff.Lines{}
		read := 0
		for _, p := range untracked {
			if ctx.Err() != nil || len(own) >= maxCountedFiles || read >= maxCountedBytes {
				break
			}
			b, err := readWorkTreeFile(r.WorkTree, p, min(maxCountedFile, maxCountedBytes-read))
			if err != nil {
				continue
			}
			read += len(b)
			own[p] = diff.CountText(b)
		}
		if ctx.Err() != nil {
			cancel()
			return
		}
		out := ChangeLines{For: st, Staged: make([]diff.Lines, st.Len()), Unstaged: make([]diff.Lines, st.Len())}
		for i := range st.Len() {
			path := st.Path(i)
			switch st.Kind(i) {
			case status.Untracked:
				out.Unstaged[i] = own[string(path)]
			case status.Changed, status.Renamed:
				old := path
				if x, _ := st.XY(i); st.Kind(i) == status.Renamed && x == 'R' {
					old = st.OrigPath(i)
				}
				out.Staged[i] = inIndex.Find(old, path)
				out.Unstaged[i] = inTree.Find(path, path)
			}
		}
		out.order = make([]int32, st.Len())
		for i := range out.order {
			out.order[i] = int32(i)
		}
		slices.SortFunc(out.order, func(a, b int32) int { return bytes.Compare(st.Path(int(a)), st.Path(int(b))) })
		s.update(func() {
			live := ctx.Err() == nil
			cancel()
			if live {
				done(out)
			}
		})
	})
}
