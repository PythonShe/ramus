package ui

import (
	"bytes"
	"slices"
	"sort"
	"strings"

	"ramus/internal/git/status"
	"ramus/internal/ui/pathtree"
)

// section is a part of the changes list.
type section uint8

const (
	secConflicts section = iota
	secStaged
	secUnstaged
	sections
)

func (s section) title() string {
	return [...]string{"Conflicts", "Staged", "Changes"}[s]
}

type rowKind uint8

const (
	rowHeader rowKind = iota
	rowFile
	rowDir
)

// changeRow is one row of the list, pointer-free: for a file, idx is its
// status entry; for a folder, its node in the section's tree.
type changeRow struct {
	kind  rowKind
	sec   section
	depth uint16
	idx   int32
}

// changeModel is the changes list built from one status, off the main
// thread: per section, the entries in path order, and the rows shown.
type changeModel struct {
	st     *status.Status
	filter string
	tree   bool
	files  [sections][]int32
	trees  [sections]*pathtree.Tree
	// leaves are a section's entries in the order its tree was built
	// from: by path with an untracked folder's trailing slash dropped,
	// so the folder is one row.
	leaves [sections][]int32
	// dirs holds where each tree node's full path is in dirBuf, for
	// folder rows: flat, not a string per folder.
	dirs   [sections][]dirSpan
	dirBuf []byte
	rows   []changeRow
	// staged counts the staged entries, the filter aside, for the commit
	// box.
	staged int
}

type dirSpan struct{ start, end uint32 }

// dir returns the path of folder node n of sec.
func (m *changeModel) dir(sec section, n int32) string {
	d := m.dirs[sec][n]
	return string(m.dirBuf[d.start:d.end])
}

// rowKey identifies a row across rebuilds, for selection.
type rowKey struct {
	sec  section
	kind rowKind
	path string
}

// openKey names a folder shown open.
type openKey struct {
	sec  section
	path string
}

// inSection reports whether status entry i belongs in sec.
func inSection(st *status.Status, i int, sec section) bool {
	switch st.Kind(i) {
	case status.Unmerged:
		return sec == secConflicts
	case status.Untracked:
		return sec == secUnstaged
	case status.Ignored:
		return false
	}
	x, y := st.XY(i)
	if sec == secStaged {
		return x != '.'
	}
	return sec == secUnstaged && y != '.'
}

// buildChanges sorts the entries of st into sections, keeping those whose
// path holds filter (ignoring ASCII case), and lays out the rows.
func buildChanges(st *status.Status, filter string, tree bool, open map[openKey]bool) *changeModel {
	m := &changeModel{st: st, filter: filter, tree: tree}
	q := []byte(strings.ToLower(filter))
	for i := range st.Len() {
		if inSection(st, i, secStaged) {
			m.staged++
		}
		if len(q) > 0 && !containsFold(st.Path(i), q) {
			continue
		}
		for sec := range sections {
			if inSection(st, i, sec) {
				m.files[sec] = append(m.files[sec], int32(i))
			}
		}
	}
	for sec := range sections {
		f := m.files[sec]
		// git lists tracked entries, then untracked ones, each sorted.
		if !sort.SliceIsSorted(f, func(a, b int) bool { return bytes.Compare(st.Path(int(f[a])), st.Path(int(f[b]))) < 0 }) {
			slices.SortStableFunc(f, func(a, b int32) int { return bytes.Compare(st.Path(int(a)), st.Path(int(b))) })
		}
		if tree && len(f) > 0 {
			leaf := func(i int32) []byte { return bytes.TrimSuffix(st.Path(int(i)), []byte("/")) }
			order := slices.Clone(f)
			slices.SortStableFunc(order, func(a, b int32) int { return bytes.Compare(leaf(a), leaf(b)) })
			m.leaves[sec] = order
			size := 0
			for _, i := range order {
				size += len(st.Path(int(i))) + 1
			}
			all := make([]byte, 0, size)
			for _, i := range order {
				all = append(append(all, leaf(i)...), 0)
			}
			t := pathtree.Build(all, 0)
			m.trees[sec] = t
			m.dirs[sec] = make([]dirSpan, t.Len())
			m.dirBuf = slices.Grow(m.dirBuf, size)
			var walk func(n pathtree.Node, parent dirSpan)
			walk = func(n pathtree.Node, parent dirSpan) {
				for _, k := range t.Children(n) {
					if !t.IsDir(k) {
						continue
					}
					start := uint32(len(m.dirBuf))
					if parent.end > parent.start {
						m.dirBuf = append(m.dirBuf, m.dirBuf[parent.start:parent.end]...)
						m.dirBuf = append(m.dirBuf, '/')
					}
					m.dirBuf = append(m.dirBuf, t.Name(k)...)
					d := dirSpan{start, uint32(len(m.dirBuf))}
					m.dirs[sec][k] = d
					walk(k, d)
				}
			}
			walk(pathtree.Root, dirSpan{})
		}
	}
	m.layout(open)
	return m
}

// layout makes the rows: a header per section with entries, then its
// files, or its tree with the folders in open shown open.
func (m *changeModel) layout(open map[openKey]bool) {
	m.rows = m.rows[:0]
	for sec := range sections {
		f := m.files[sec]
		if len(f) == 0 {
			continue
		}
		m.rows = append(m.rows, changeRow{kind: rowHeader, sec: sec})
		t := m.trees[sec]
		if t == nil {
			for _, i := range f {
				m.rows = append(m.rows, changeRow{kind: rowFile, sec: sec, idx: i})
			}
			continue
		}
		var walk func(n pathtree.Node, depth uint16)
		walk = func(n pathtree.Node, depth uint16) {
			for _, k := range t.Children(n) {
				if t.IsDir(k) {
					m.rows = append(m.rows, changeRow{kind: rowDir, sec: sec, depth: depth, idx: int32(k)})
					if len(open) > 0 && open[openKey{sec, m.dir(sec, int32(k))}] {
						walk(k, depth+1)
					}
					continue
				}
				m.rows = append(m.rows, changeRow{kind: rowFile, sec: sec, depth: depth, idx: m.leaves[sec][t.Index(k)]})
			}
		}
		walk(pathtree.Root, 0)
	}
}

func (m *changeModel) key(r changeRow) rowKey {
	switch r.kind {
	case rowFile:
		return rowKey{r.sec, rowFile, string(m.st.Path(int(r.idx)))}
	case rowDir:
		return rowKey{r.sec, rowDir, m.dir(r.sec, r.idx)}
	}
	return rowKey{sec: r.sec}
}

// name is what a row shows: the whole path in a flat list, the last
// part in a tree.
func (m *changeModel) name(r changeRow) string {
	switch r.kind {
	case rowDir:
		return m.trees[r.sec].Name(pathtree.Node(r.idx))
	case rowFile:
		p := string(m.st.Path(int(r.idx)))
		if m.tree {
			// An untracked folder keeps its slash.
			if i := strings.LastIndexByte(strings.TrimSuffix(p, "/"), '/'); i >= 0 {
				return p[i+1:]
			}
		}
		return p
	}
	return r.sec.title()
}

// under returns the entries of sec at or below path, a file or a folder:
// a range of the section's sorted entries.
func (m *changeModel) under(sec section, path string, dir bool) []int32 {
	f := m.files[sec]
	if !dir {
		i, ok := slices.BinarySearchFunc(f, path, func(e int32, p string) int { return bytes.Compare(m.st.Path(int(e)), []byte(p)) })
		if !ok {
			return nil
		}
		return f[i : i+1]
	}
	prefix := []byte(path + "/")
	lo := sort.Search(len(f), func(i int) bool { return bytes.Compare(m.st.Path(int(f[i])), prefix) >= 0 })
	hi := lo
	for hi < len(f) && bytes.HasPrefix(m.st.Path(int(f[hi])), prefix) {
		hi++
	}
	return f[lo:hi]
}

// containsFold reports whether s holds q, which is lower case, ignoring
// ASCII case.
func containsFold(s, q []byte) bool {
	if len(q) == 0 {
		return true
	}
	for i := 0; i+len(q) <= len(s); i++ {
		j := 0
		for ; j < len(q); j++ {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != q[j] {
				break
			}
		}
		if j == len(q) {
			return true
		}
	}
	return false
}
