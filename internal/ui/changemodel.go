package ui

import (
	"bytes"
	"slices"
	"sort"
	"strings"

	"ramus/internal/git/status"
	"ramus/internal/session"
	"ramus/internal/ui/pathtree"
	"ramus/locales"
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
	switch s {
	case secConflicts:
		return locales.ChangesSectionConflicts()
	case secStaged:
		return locales.ChangesSectionStaged()
	}
	return locales.ChangesSectionChanges()
}

// header heads the section, with the n files in it.
func (s section) header(n int) string {
	switch s {
	case secConflicts:
		return locales.ChangesSectionConflictsCount(n)
	case secStaged:
		return locales.ChangesSectionStagedCount(n)
	}
	return locales.ChangesSectionChangesCount(n)
}

type rowKind uint8

const (
	rowHeader rowKind = iota
	rowFile
	rowDir
	// rowInner is a file in a folder git does not track, which status
	// lists whole, and rowMore says how many more it holds than are
	// listed.
	rowInner
	rowMore
)

// changeRow is one row of the list, pointer-free: for a file, idx is its
// status entry; for a folder, its node in the section's tree; for a file
// in an untracked folder, idx is the folder's entry and sub the file's
// place among those listed.
type changeRow struct {
	kind  rowKind
	sec   section
	depth uint16
	idx   int32
	sub   int32
}

// changeModel is the changes list built from one status, off the main
// thread: per section, the entries in path order, and the rows shown.
type changeModel struct {
	st     *status.Status
	filter string
	// q is filter in lower case.
	q    []byte
	tree bool
	// folders are the files of the untracked folders, listed by a count
	// of st or of a status before it, for rowInner.
	folders map[string]session.FolderFiles
	files   [sections][]int32
	trees   [sections]*pathtree.Tree
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

// openKey names a folder of a section's tree.
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
// path holds filter (ignoring ASCII case), and lays out the rows, with
// the folders in closed shown closed, and the untracked folders open
// holding the files of folders.
func buildChanges(st *status.Status, filter string, tree bool, closed map[openKey]bool, folders map[string]session.FolderFiles) *changeModel {
	// Lowered as containsFold lowers the paths, in ASCII alone: lowered
	// whole, a capital past ASCII would match none of the paths holding it.
	q := bytes.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, []byte(filter))
	m := &changeModel{st: st, filter: filter, q: q, tree: tree, folders: folders}
	if len(folders) > 0 {
		m.folders = m.untracked(folders)
	}
	for i := range st.Len() {
		if inSection(st, i, secStaged) {
			m.staged++
		}
		if len(q) > 0 && !containsFold(st.Path(i), q) && !m.holds(i, q) {
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
	m.layout(closed, q)
	return m
}

// untracked is folders without the files st now lists as tracked: those
// of a count of an earlier status, from before one was staged, which
// stays the folders' listing until the next count.
func (m *changeModel) untracked(folders map[string]session.FolderFiles) map[string]session.FolderFiles {
	dirs := make([]string, 0, len(folders))
	for d := range folders {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	var tracked map[string]bool
	for i := range m.st.Len() {
		if m.st.Kind(i) == status.Untracked {
			continue
		}
		p := m.st.Path(i)
		k, _ := slices.BinarySearchFunc(dirs, p, func(d string, p []byte) int { return bytes.Compare([]byte(d), p) })
		if k > 0 && bytes.HasPrefix(p, []byte(dirs[k-1])) {
			if tracked == nil {
				tracked = map[string]bool{}
			}
			tracked[string(p)] = true
		}
	}
	if tracked == nil {
		return folders
	}
	out := make(map[string]session.FolderFiles, len(folders))
	for d, f := range folders {
		kept := session.FolderFiles{More: f.More}
		for k, p := range f.Paths {
			if !tracked[p] {
				kept.Paths = append(kept.Paths, p)
				kept.Lines = append(kept.Lines, f.Lines[k])
			}
		}
		out[d] = kept
	}
	return out
}

// holds reports whether entry i is an untracked folder holding a file
// listed whose path holds q.
func (m *changeModel) holds(i int, q []byte) bool {
	f, ok := m.listed(int32(i))
	if !ok {
		return false
	}
	for _, p := range f.Paths {
		if containsFold([]byte(p), q) {
			return true
		}
	}
	return false
}

// listed returns the files of entry i, an untracked folder, once listed.
func (m *changeModel) listed(i int32) (session.FolderFiles, bool) {
	if !untrackedFolder(m.st, int(i)) {
		return session.FolderFiles{}, false
	}
	f, ok := m.folders[string(m.st.Path(int(i)))]
	return f, ok
}

// opens reports whether entry i is an untracked folder that opens to
// show its files: not a repository inside the work tree, which git lists
// as a folder too, with nothing in it listed.
func (m *changeModel) opens(i int32) bool {
	if !untrackedFolder(m.st, int(i)) {
		return false
	}
	f, ok := m.listed(i)
	return !ok || len(f.Paths) != 1 || f.Paths[0] != string(m.st.Path(int(i)))
}

// isFolder reports whether r is an untracked folder that opens.
func (m *changeModel) isFolder(r changeRow) bool { return r.kind == rowFile && m.opens(r.idx) }

// folderKey names the untracked folder of entry i, for closed.
func (m *changeModel) folderKey(sec section, i int32) openKey {
	return openKey{sec, string(m.st.Path(int(i)))}
}

// innerRows adds the rows of the files listed of folder entry i shown at
// depth, those whose path holds q when the folder's does not.
func (m *changeModel) innerRows(sec section, i int32, depth uint16, q []byte) {
	f, ok := m.listed(i)
	if !ok || !m.opens(i) {
		return
	}
	all := len(q) == 0 || containsFold(m.st.Path(int(i)), q)
	for k, p := range f.Paths {
		if all || containsFold([]byte(p), q) {
			m.rows = append(m.rows, changeRow{kind: rowInner, sec: sec, depth: depth, idx: i, sub: int32(k)})
		}
	}
	if f.More > 0 {
		m.rows = append(m.rows, changeRow{kind: rowMore, sec: sec, depth: depth, idx: i})
	}
}

// innerPath is the path of r, a rowInner.
func (m *changeModel) innerPath(r changeRow) string {
	f, _ := m.listed(r.idx)
	return f.Paths[r.sub]
}

// path is the path of the file or folder r shows.
func (m *changeModel) path(r changeRow) string {
	switch r.kind {
	case rowFile, rowMore:
		return string(m.st.Path(int(r.idx)))
	case rowInner:
		return m.innerPath(r)
	case rowDir:
		return m.dir(r.sec, r.idx)
	}
	return ""
}

// layout makes the rows: a header per section with entries, then its
// files, or its tree with every folder open but those in closed: the
// files are what the user came to see, as in the flat list. An untracked
// folder open has the files listed of it, those whose path holds q, under
// it.
func (m *changeModel) layout(closed map[openKey]bool, q []byte) {
	m.rows = m.rows[:0]
	for sec := range sections {
		f := m.files[sec]
		if len(f) == 0 {
			continue
		}
		m.rows = append(m.rows, changeRow{kind: rowHeader, sec: sec})
		t := m.trees[sec]
		file := func(i int32, depth uint16) {
			m.rows = append(m.rows, changeRow{kind: rowFile, sec: sec, depth: depth, idx: i})
			if untrackedFolder(m.st, int(i)) && !closed[m.folderKey(sec, i)] {
				m.innerRows(sec, i, depth+1, q)
			}
		}
		if t == nil {
			for _, i := range f {
				file(i, 0)
			}
			continue
		}
		var walk func(n pathtree.Node, depth uint16)
		walk = func(n pathtree.Node, depth uint16) {
			for _, k := range t.Children(n) {
				if t.IsDir(k) {
					m.rows = append(m.rows, changeRow{kind: rowDir, sec: sec, depth: depth, idx: int32(k)})
					if len(closed) == 0 || !closed[openKey{sec, m.dir(sec, int32(k))}] {
						walk(k, depth+1)
					}
					continue
				}
				file(m.leaves[sec][t.Index(k)], depth)
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
	case rowInner, rowMore:
		return rowKey{r.sec, r.kind, m.path(r)}
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
	case rowInner:
		// Named from the folder, the row above.
		return strings.TrimPrefix(m.innerPath(r), string(m.st.Path(int(r.idx))))
	}
	return r.sec.title()
}

// all returns the entries of sec, those the filter hides too, for what
// says it acts on all.
func (m *changeModel) all(sec section) []int32 {
	if m.filter == "" {
		return m.files[sec]
	}
	var out []int32
	for i := range m.st.Len() {
		if inSection(m.st, i, sec) {
			out = append(out, int32(i))
		}
	}
	return out
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
	lo, hi := m.folderRange(sec, path)
	return f[lo:hi]
}

// folderRange is where the entries of sec in folder path are among the
// section's sorted entries, f[lo:hi].
func (m *changeModel) folderRange(sec section, path string) (lo, hi int) {
	f := m.files[sec]
	// The paths in it run from "path/" up to "path0", '0' following '/':
	// both ends are found by a search, for a folder of the tree shown
	// each frame.
	prefix := []byte(path + "/")
	end := []byte(path + "0")
	lo = sort.Search(len(f), func(i int) bool { return bytes.Compare(m.st.Path(int(f[i])), prefix) >= 0 })
	hi = lo + sort.Search(len(f)-lo, func(i int) bool { return bytes.Compare(m.st.Path(int(f[lo+i])), end) >= 0 })
	return lo, hi
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
