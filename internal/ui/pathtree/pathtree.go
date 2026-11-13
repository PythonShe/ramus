// Package pathtree turns a sorted list of slash-separated paths, such as
// the files of a status or a commit, into a tree of directories and files
// for a tree view, in flat pointer-free storage: a 390,000-file work tree
// is a handful of slices, not 390,000 objects.
package pathtree

import (
	"bytes"
	"sort"
)

// Node identifies a directory or file; Root is the tree's root.
type Node uint32

// Root is the unnamed directory that holds the top-level entries.
const Root Node = 0

// Tree is an immutable tree of paths.
type Tree struct {
	names []byte   // every name, back to back
	ends  []uint32 // per node, the end of its name in names
	// leaf is the index of the input path a file came from, or -1 for a
	// directory.
	leaf []int32
	// kids holds every directory's children, directories first, each
	// sorted by name; a node's children are kids[first[n]:first[n+1]].
	kids  []Node
	first []uint32
	files int
}

// Build makes a tree of paths, given back to back with a separator after
// each (a NUL from git's -z output, or a newline). Paths must be sorted
// bytewise, as git sorts them; duplicates are kept once.
func Build(paths []byte, sep byte) *Tree {
	t := &Tree{}
	n := bytes.Count(paths, []byte{sep})
	t.names = make([]byte, 0, len(paths)/4)
	t.ends = make([]uint32, 0, n+n/8+1)
	t.leaf = make([]int32, 0, n+n/8+1)
	parent := make([]Node, 0, n+n/8+1)
	add := func(name []byte, p Node, leaf int32) Node {
		t.names = append(t.names, name...)
		t.ends = append(t.ends, uint32(len(t.names)))
		t.leaf = append(t.leaf, leaf)
		parent = append(parent, p)
		return Node(len(t.ends) - 1)
	}
	add(nil, Root, -1)
	// stack holds the open directories of the previous path, with the
	// byte length of the path prefix each one covers.
	type open struct {
		node Node
		end  int
	}
	stack := []open{{Root, 0}}
	var prev []byte
	index := int32(0)
	for len(paths) > 0 {
		i := bytes.IndexByte(paths, sep)
		if i < 0 {
			i = len(paths)
		}
		p := paths[:i]
		paths = paths[min(i+1, len(paths)):]
		if len(p) == 0 || bytes.Equal(p, prev) {
			index++
			continue
		}
		// Keep the directories this path shares with the previous one.
		for len(stack) > 1 {
			top := stack[len(stack)-1]
			if top.end <= len(p) && bytes.Equal(p[:top.end], prev[:top.end]) && p[top.end-1] == '/' {
				break
			}
			stack = stack[:len(stack)-1]
		}
		start := stack[len(stack)-1].end
		for {
			j := bytes.IndexByte(p[start:], '/')
			if j < 0 {
				break
			}
			d := add(p[start:start+j], stack[len(stack)-1].node, -1)
			start += j + 1
			stack = append(stack, open{d, start})
		}
		add(p[start:], stack[len(stack)-1].node, index)
		t.files++
		index++
		prev = p
	}
	t.link(parent)
	return t
}

// link groups children by parent with a counting sort, then orders each
// group directories first.
func (t *Tree) link(parent []Node) {
	n := len(parent)
	t.first = make([]uint32, n+1)
	for i := 1; i < n; i++ {
		t.first[parent[i]+1]++
	}
	for i := 1; i <= n; i++ {
		t.first[i] += t.first[i-1]
	}
	t.kids = make([]Node, n-1)
	fill := append([]uint32(nil), t.first[:n]...)
	for i := 1; i < n; i++ {
		p := parent[i]
		t.kids[fill[p]] = Node(i)
		fill[p]++
	}
	for d := 0; d < n; d++ {
		k := t.kids[t.first[d]:t.first[d+1]]
		if len(k) < 2 {
			continue
		}
		// Input order is bytewise by full path, which already sorts names
		// within a directory, except that "a/x" sorts after "a.txt": put
		// directories first, keeping each group's order.
		sort.SliceStable(k, func(a, b int) bool {
			return t.IsDir(k[a]) && !t.IsDir(k[b])
		})
	}
}

// Len returns the number of nodes, the root included.
func (t *Tree) Len() int { return len(t.ends) }

// Files returns the number of files.
func (t *Tree) Files() int { return t.files }

// Name returns a node's own name, without its directory.
func (t *Tree) Name(n Node) string {
	start := uint32(0)
	if n > 0 {
		start = t.ends[n-1]
	}
	return string(t.names[start:t.ends[n]])
}

// IsDir reports whether a node is a directory.
func (t *Tree) IsDir(n Node) bool { return t.leaf[n] < 0 }

// Index returns the position, in the input, of a file's path, or -1 for a
// directory.
func (t *Tree) Index(n Node) int { return int(t.leaf[n]) }

// Children returns a directory's children, directories first. The slice
// belongs to the tree.
func (t *Tree) Children(n Node) []Node {
	return t.kids[t.first[n]:t.first[n+1]:t.first[n+1]]
}

// Bytes returns the memory the tree holds.
func (t *Tree) Bytes() int {
	return cap(t.names) + cap(t.ends)*4 + cap(t.leaf)*4 + cap(t.kids)*4 + cap(t.first)*4
}
