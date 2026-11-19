package repo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ramus/internal/git/config"
	"ramus/internal/git/index"
	"ramus/internal/git/pathsafe"
	"ramus/internal/git/refs"
)

// maxPackedRefs bounds the packed-refs read for replace refs; one larger
// counts as holding some, as one that cannot be read does.
const maxPackedRefs = 1 << 30

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
	// Shallow, Grafts, ReplaceRefs and CommitGraphOff change while the
	// repository is open; Repository.Capabilities reads them again.
	// Unknown lists extensions this version does not know; git refuses
	// such a repository unless it knows them, so all of it goes to git.
	Unknown []string
	// FormatVersion is core.repositoryFormatVersion: 0 or 1, as git
	// supports; past 1 git refuses the repository and so does the engine.
	FormatVersion int
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
	c := Capabilities{HashSize: 20, ObjectFormat: "sha1", RefStorage: "files", FormatVersion: cfg.FormatVersion}
	// git reads the format from the repository's own config alone, and
	// in version 0 honours only the extensions that predate version 1;
	// the others it ignores, or refuses the repository for (which
	// cfg.UnknownExtensions reports).
	exts := map[string]string{}
	version := 0
	for _, e := range cfg.Entries() {
		if e.Scope != config.Local {
			continue
		}
		if name, ok := strings.CutPrefix(e.Key, "extensions."); ok {
			exts[name] = e.Value
		} else if e.Key == "core.repositoryformatversion" {
			version, _ = strconv.Atoi(strings.TrimSpace(e.Value))
		}
	}
	c.FormatVersion = max(c.FormatVersion, version)
	v1 := c.FormatVersion >= 1
	if v, ok := exts["objectformat"]; ok && v1 && strings.EqualFold(v, "sha256") {
		c.HashSize, c.ObjectFormat = 32, "sha256"
	}
	if v, ok := exts["refstorage"]; ok && v1 {
		c.RefStorage = strings.ToLower(v)
	}
	if _, ok := exts["partialclone"]; ok {
		c.PartialClone = true
	}
	for _, remote := range cfg.Subsections("remote", "promisor") {
		if cfg.Bool("remote."+remote+".promisor", false) {
			c.PartialClone = true
		}
	}
	if v1 {
		for name := range exts {
			if !known[name] {
				c.Unknown = append(c.Unknown, name)
			}
		}
		slices.Sort(c.Unknown)
	}
	for _, name := range cfg.UnknownExtensions {
		if !slices.Contains(c.Unknown, name) {
			c.Unknown = append(c.Unknown, name)
		}
	}
	c.checkHistory(loc, cfg, nil)
	if cfg.Bool("core.splitindex", false) {
		c.SplitIndex = true
	}
	if x, err := index.ReadFile(filepath.Join(loc.GitDir, "index"), c.HashSize); err == nil {
		c.SplitIndex = c.SplitIndex || x.Split()
		c.SparseIndex = x.Sparse()
	}
	return c
}

// checkHistory reads what decides whether the commit-graph describes the
// history git shows, which can change while the repository is open: a
// shallow fetch, git replace, grafts, the config. seen, when not nil,
// keeps what the last check of replace refs read.
func (c *Capabilities) checkHistory(loc Location, cfg *config.Config, seen *replaceSeen) {
	c.CommitGraphOff = !cfg.Bool("core.commitgraph", true)
	c.Shallow = exists(filepath.Join(loc.CommonDir, "shallow"))
	c.Grafts = exists(graftFile(loc))
	c.ReplaceRefs = cfg.Bool("core.usereplacerefs", true) && os.Getenv("GIT_NO_REPLACE_OBJECTS") == "" && hasReplaceRefs(loc, *c, seen)
}

// graftFile is where git reads grafts from.
func graftFile(loc Location) string {
	if f := os.Getenv("GIT_GRAFT_FILE"); f != "" {
		return f
	}
	return filepath.Join(loc.CommonDir, "info", "grafts")
}

// formatKnown reports whether git would work in the repository at all,
// and the engine with it.
func (c Capabilities) formatKnown() bool { return len(c.Unknown) == 0 && c.FormatVersion <= 1 }

// NativeRefs reports whether the engine's own ref readers (files or
// reftable) serve the repository.
func (c Capabilities) NativeRefs() bool {
	return (c.RefStorage == "files" || c.RefStorage == "reftable") && c.formatKnown()
}

// NativeIndex reports whether the index reader serves the work tree.
func (c Capabilities) NativeIndex() bool {
	return !c.SplitIndex && !c.SparseIndex && c.HashSize == 20 && c.formatKnown()
}

// NativeObjects reports whether objects may be read without asking git
// where they are: a partial clone's missing objects must not be guessed
// at, and SHA-256 waits for the readers to be tested on it.
func (c Capabilities) NativeObjects() bool {
	return !c.PartialClone && c.HashSize == 20 && c.formatKnown()
}

// CommitGraphUsable reports whether a commit-graph, if present, describes
// the history git would show.
func (c Capabilities) CommitGraphUsable() bool {
	return !c.CommitGraphOff && !c.Grafts && !c.ReplaceRefs && !c.Shallow && c.formatKnown()
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
	add(c.RefStorage != "files" && c.RefStorage != "reftable", c.RefStorage+" refs")
	add(c.PartialClone, "partial clone")
	add(c.SplitIndex, "split index")
	add(c.SparseIndex, "sparse index")
	add(c.Shallow, "shallow history")
	add(c.Grafts, "grafts")
	add(c.ReplaceRefs, "replace refs")
	add(c.FormatVersion > 1, "repository format version "+strconv.Itoa(c.FormatVersion))
	for _, u := range c.Unknown {
		out = append(out, "extension "+u)
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// replaceBase is where git looks for replace refs.
func replaceBase() string {
	b := os.Getenv("GIT_REPLACE_REF_BASE")
	if b == "" {
		return "refs/replace/"
	}
	if !strings.HasSuffix(b, "/") {
		b += "/"
	}
	return b
}

// replaceSeen is what a check of replace refs read, kept so the next
// reads again only what changed since: packed-refs, or the reftable
// stack's list, by stamp.
type replaceSeen struct {
	at    fileStamp
	base  string
	found bool
	ok    bool
}

// hasReplaceRefs reports whether the repository holds a replace ref. One
// that cannot be told counts as holding some, so the commit-graph, which
// knows nothing of them, is not trusted.
func hasReplaceRefs(loc Location, c Capabilities, seen *replaceSeen) bool {
	base := replaceBase()
	if seen == nil {
		seen = &replaceSeen{}
	}
	if c.RefStorage == "reftable" {
		at := stamp(filepath.Join(loc.CommonDir, "reftable", "tables.list"))
		if !seen.ok || seen.at != at || seen.base != base {
			found, err := refs.NewReftable(loc.GitDir, loc.CommonDir, c.HashSize).HasShared(base)
			*seen = replaceSeen{at: at, base: base, found: found || err != nil, ok: err == nil}
		}
		return seen.found
	}
	found := false
	filepath.WalkDir(filepath.Join(loc.CommonDir, filepath.FromSlash(base)), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if found {
		return true
	}
	packed := filepath.Join(loc.CommonDir, "packed-refs")
	at := stamp(packed)
	if seen.ok && seen.at == at && seen.base == base {
		return seen.found
	}
	b, err := pathsafe.ReadFile(packed, maxPackedRefs)
	if err != nil {
		found = !errors.Is(err, fs.ErrNotExist)
	} else {
		found = strings.Contains(string(b), " "+base)
	}
	*seen = replaceSeen{at: at, base: base, found: found, ok: err == nil || errors.Is(err, fs.ErrNotExist)}
	return found
}
