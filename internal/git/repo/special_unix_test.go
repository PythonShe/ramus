//go:build !windows

package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ramus/internal/git/commitgraph"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/index"
	"ramus/internal/git/lfs"
	"ramus/internal/git/refs"
)

// returns fails the test when fn does not return in time: a read of a
// named pipe would wait for a writer for ever.
func returns(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatalf("%s still waiting", what)
	}
}

func fifo(t *testing.T, path string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.Remove(path)
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

// An excludes file that is a device or a named pipe is not read: hiding
// folders returns at once, trusted or not, and for a repository not
// trusted a path its own config names is not followed at all.
func TestExcludesFileThatIsNoFile(t *testing.T) {
	for _, trusted := range []bool{true, false} {
		g := gittest.Init(t)
		g.Write("f", "x")
		g.Commit("one")
		g.Write("node_modules/a.js", "x")
		pipe := filepath.Join(t.TempDir(), "pipe")
		fifo(t, pipe)
		os.Symlink("/dev/zero", filepath.Join(g.Dir, "zero"))
		for _, source := range []string{"/dev/zero", pipe, "zero"} {
			g.Git("config", "core.excludesFile", source)
			r, err := Open(context.Background(), testrun.Runner(t), g.Dir, func(Location) bool { return trusted })
			if err != nil {
				t.Fatal(err)
			}
			returns(t, "hiding folders with "+source+" as the excludes file", func() {
				if _, err := r.HideFolders(DependencyFolders); err != nil {
					t.Error(err)
				}
			})
			r.Close()
		}
	}
}

// A named pipe in place of a file of the git directory is not a file to
// read: discovery and the readers of the git directory return at once.
func TestNamedPipesInTheGitDirectory(t *testing.T) {
	r := gittest.Init(t)
	r.Write("f", "x")
	r.Commit("one")
	// Read once: git itself waits on a named pipe as commondir.
	gd := r.GitDir()

	head := filepath.Join(gd, "HEAD")
	fifo(t, head)
	returns(t, "discovery of a named pipe as HEAD", func() {
		if _, err := Discover(r.Dir); err == nil {
			t.Error("a named pipe as HEAD is a git directory")
		}
	})
	os.Remove(head)
	os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644)

	fifo(t, filepath.Join(gd, "commondir"))
	returns(t, "discovery of a named pipe as commondir", func() { Discover(r.Dir) })
	os.Remove(filepath.Join(gd, "commondir"))

	fifo(t, filepath.Join(gd, "index"))
	returns(t, "the index's entry count", func() {
		if n := indexEntries(filepath.Join(gd, "index")); n != -1 {
			t.Errorf("a named pipe holds %d entries", n)
		}
	})

	fifo(t, filepath.Join(gd, "sequencer", "todo"))
	returns(t, "the operation in progress", func() {
		if op := InProgress(gd); op != CherryPick {
			t.Errorf("got %q", op)
		}
	})
}

// Every reader of a file in the git directory, and of the work tree's
// .gitattributes, refuses a named pipe in its place at once.
func TestNamedPipesForEveryReader(t *testing.T) {
	r := gittest.Init(t)
	r.Write("f", "x")
	r.Commit("one")
	gd := r.GitDir()
	loc, err := Discover(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	objects := filepath.Join(gd, "objects")
	oid := strings.Repeat("ab", 32)
	for _, p := range []string{
		filepath.Join(gd, "index"),
		filepath.Join(gd, "packed-refs"),
		filepath.Join(gd, "reftable", "tables.list"),
		filepath.Join(objects, "info", "commit-graph"),
		filepath.Join(gd, "lfs", "objects", oid[:2], oid[2:4], oid),
		filepath.Join(r.Dir, ".gitattributes"),
	} {
		fifo(t, p)
	}
	returns(t, "the index", func() {
		if _, err := index.ReadFile(filepath.Join(gd, "index"), 20); err == nil {
			t.Error("read a named pipe as the index")
		}
		if n := indexEntries(filepath.Join(gd, "index")); n != -1 {
			t.Errorf("a named pipe holds %d entries", n)
		}
	})
	returns(t, "packed-refs", func() {
		if _, err := refs.New(gd, gd, 20).List(); err == nil {
			t.Error("read a named pipe as packed-refs")
		}
		if !hasReplaceRefs(loc, Capabilities{RefStorage: "files"}, nil) {
			t.Error("a packed-refs that cannot be read holds no replace refs")
		}
	})
	returns(t, "the reftable stack", func() {
		if _, err := refs.NewReftable(gd, gd, 20).List(); err == nil {
			t.Error("read a named pipe as tables.list")
		}
	})
	returns(t, "the commit-graph", func() {
		if g, err := commitgraph.Open(objects); err == nil {
			g.Close()
			t.Error("mapped a named pipe as the commit-graph")
		}
	})
	fifo(t, filepath.Join(objects, "info", "commit-graphs", "commit-graph-chain"))
	returns(t, "the commit-graph chain", func() {
		if g, err := commitgraph.Open(objects); err == nil {
			g.Close()
			t.Error("read a named pipe as the commit-graph chain")
		}
	})
	returns(t, "an LFS object", func() {
		if _, err := lfs.ReadObject(gd, lfs.Pointer{OID: oid, Size: 1}, 1<<20); err == nil {
			t.Error("read a named pipe as an LFS object")
		}
	})
	os.RemoveAll(filepath.Join(gd, "lfs"))
	returns(t, ".gitattributes", func() {
		if lfs.Uses(gd, r.Dir) {
			t.Error("a named pipe as .gitattributes routes files through LFS")
		}
	})
}
