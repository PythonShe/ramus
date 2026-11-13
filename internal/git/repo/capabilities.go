package repo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ramus/internal/git/config"
	"ramus/internal/git/index"
)

// Capabilities says which of a repository's parts the engine's own readers
// can serve. What they cannot goes to git, never to a guess.
type Capabilities struct {
	// HashSize is 20 for SHA-1, 32 for SHA-256.
	HashSize     int
	ObjectFormat string // "sha1" or "sha256"
	RefStorage   string // "files" or "reftable"
	PartialClone bool
	Shallow      bool
	SplitIndex   bool
	SparseIndex  bool
	// Grafts and ReplaceRefs change what a commit's parents are, so the
	// commit-graph is not used while either is present.
	Grafts      bool
	ReplaceRefs bool
	// CommitGraphOff is core.commitGraph=false.
	CommitGraphOff bool
	// Unknown lists extensions this version does not know; git refuses
	// such a repository unless it knows them, so all of it goes to git.
	Unknown []string
}

// known are the repository extensions the engine understands, or that do
// not affect what it reads.
var known = map[string]bool{
	"objectformat": true, "refstorage": true, "partialclone": true, "worktreeconfig": true,
	"preciousobjects": true, "noop": true, "noop-v1": true, "compatobjectformat": true,
	"submodulepathconfig": true, "relativeworktrees": true,
}

// Check reads what the repository at loc needs, from its config (read
// through git) and the files in its git directory.
func Check(loc Location, cfg *config.Config) Capabilities {
	c := Capabilities{HashSize: 20, ObjectFormat: "sha1", RefStorage: "files"}
	if v, ok := cfg.Get("extensions.objectformat"); ok && strings.EqualFold(v, "sha256") {
		c.HashSize, c.ObjectFormat = 32, "sha256"
	}
	if v, ok := cfg.Get("extensions.refstorage"); ok {
		c.RefStorage = strings.ToLower(v)
	}
	if _, ok := cfg.Get("extensions.partialclone"); ok {
		c.PartialClone = true
	}
	for _, remote := range cfg.Subsections("remote", "promisor") {
		if cfg.Bool("remote."+remote+".promisor", false) {
			c.PartialClone = true
		}
	}
	for _, e := range cfg.Entries() {
		if name, ok := strings.CutPrefix(e.Key, "extensions."); ok && !known[name] && e.Scope != config.Command {
			c.Unknown = append(c.Unknown, name)
		}
	}
	c.Unknown = append(c.Unknown, cfg.UnknownExtensions...)
	c.CommitGraphOff = !cfg.Bool("core.commitgraph", true)
	c.Shallow = exists(filepath.Join(loc.CommonDir, "shallow"))
	c.Grafts = exists(filepath.Join(loc.CommonDir, "info", "grafts"))
	c.ReplaceRefs = hasReplaceRefs(loc.CommonDir) && cfg.Bool("core.usereplacerefs", true) && os.Getenv("GIT_NO_REPLACE_OBJECTS") == ""
	if cfg.Bool("core.splitindex", false) {
		c.SplitIndex = true
	}
	if x, err := index.ReadFile(filepath.Join(loc.GitDir, "index"), c.HashSize); err == nil {
		c.SplitIndex = c.SplitIndex || x.Split()
		c.SparseIndex = x.Sparse()
	}
	return c
}

// NativeRefs reports whether the files-format ref reader serves the
// repository.
func (c Capabilities) NativeRefs() bool { return c.RefStorage == "files" && len(c.Unknown) == 0 }

// NativeIndex reports whether the index reader serves the work tree.
func (c Capabilities) NativeIndex() bool {
	return !c.SplitIndex && !c.SparseIndex && c.HashSize == 20 && len(c.Unknown) == 0
}

// NativeObjects reports whether objects may be read without asking git
// where they are: a partial clone's missing objects must not be guessed
// at, and SHA-256 waits for the readers to be tested on it.
func (c Capabilities) NativeObjects() bool {
	return !c.PartialClone && c.HashSize == 20 && len(c.Unknown) == 0
}

// CommitGraphUsable reports whether a commit-graph, if present, describes
// the history git would show.
func (c Capabilities) CommitGraphUsable() bool {
	return !c.CommitGraphOff && !c.Grafts && !c.ReplaceRefs && !c.Shallow && len(c.Unknown) == 0
}

// Fallbacks names what goes to git, for the repository's status line.
func (c Capabilities) Fallbacks() []string {
	var out []string
	add := func(on bool, s string) {
		if on {
			out = append(out, s)
		}
	}
	add(c.HashSize == 32, "SHA-256 objects")
	add(c.RefStorage != "files", c.RefStorage+" refs")
	add(c.PartialClone, "partial clone")
	add(c.SplitIndex, "split index")
	add(c.SparseIndex, "sparse index")
	add(c.Shallow, "shallow history")
	add(c.Grafts, "grafts")
	add(c.ReplaceRefs, "replace refs")
	for _, u := range c.Unknown {
		out = append(out, "extension "+u)
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func hasReplaceRefs(common string) bool {
	found := false
	filepath.WalkDir(filepath.Join(common, "refs", "replace"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if found {
		return true
	}
	b, err := os.ReadFile(filepath.Join(common, "packed-refs"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return strings.Contains(string(b), " refs/replace/")
}
