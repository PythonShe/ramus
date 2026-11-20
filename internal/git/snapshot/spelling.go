package snapshot

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"ramus/internal/git/run"
)

// SpellingError is a path the disk holds under another spelling, on a
// volume that does not tell the two apart (letter case on macOS and
// Windows by default, Unicode form on APFS): git, matching paths as
// spelled, takes the file for one not there, so a snapshot of the path
// does not keep it, and a write of the path overwrites it.
type SpellingError struct {
	Path   string
	OnDisk string
}

func (e *SpellingError) Error() string {
	return fmt.Sprintf("snapshot: %s is on disk as %s, which this volume does not tell apart from it; rename it first", e.Path, e.OnDisk)
}

// checkSpelling refuses, with a *SpellingError, the first of paths the
// work tree at root holds under another spelling that git does not take
// for the one asked: same, when set, reports whether git takes onDisk,
// the path to an entry of p as the disk spells it, for asked. Whether a
// folder's volume tells spellings apart is asked of the volume, by
// looking the name up as another case, not assumed from the system.
func checkSpelling(root string, paths []string, same func(p, asked, onDisk string) bool) error {
	names := map[string]map[string]bool{} // a folder's entries, read once
	for _, p := range paths {
		dir, rel := root, ""
		parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
		for i, c := range parts {
			next := filepath.Join(dir, c)
			fi, err := os.Lstat(next)
			if err != nil {
				break // nothing there from here on
			}
			if needsLook(dir, c, fi) {
				in, ok := names[dir]
				if !ok {
					in = entries(dir)
					names[dir] = in
				}
				if !in[c] {
					disk := spelledOnDisk(dir, c, fi, in)
					if same == nil || !same(strings.TrimSuffix(p, "/"), path.Join(rel, c), path.Join(rel, disk)) {
						return &SpellingError{Path: p, OnDisk: path.Join(append([]string{rel, disk}, parts[i+1:]...)...)}
					}
				}
			}
			dir, rel = next, path.Join(rel, c)
		}
	}
	return nil
}

// gitTakesAs is checkSpelling's same for the repository at w. git takes
// a name the disk spells otherwise for the one asked where it reads the
// folder's names composed (core.precomposeunicode) and the two differ in
// Unicode form only. Otherwise it does only for a file the index holds
// as asked, p itself, with nothing held under the disk's spelling, as
// after a rename in case only outside git: git finds that file by the
// index's name. An untracked file in another case, or in a folder in
// another case, git takes for another file, and a write of a folder
// would write over it.
func gitTakesAs(ctx context.Context, r *run.Runner, w Where) func(p, asked, onDisk string) bool {
	precompose, read := false, false
	// listed are what the index holds under a path, by the path.
	listed := map[string][]string{}
	list := func(q string) []string {
		if l, ok := listed[q]; ok {
			return l
		}
		res, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true, Env: []string{"GIT_LITERAL_PATHSPECS=1"},
			Args: []string{"ls-files", "-z", "--cached", "--full-name", "--", q}})
		var l []string
		if err == nil {
			// Compared byte for byte after: git may match a pathspec
			// ignoring case.
			l = strings.Split(strings.TrimSuffix(string(res.Stdout), "\x00"), "\x00")
		}
		listed[q] = l
		return l
	}
	return func(p, asked, onDisk string) bool {
		if !read {
			res, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true, Args: []string{"config", "--type=bool", "core.precomposeunicode"}})
			precompose, read = err == nil && strings.TrimSpace(string(res.Stdout)) == "true", true
		}
		if precompose && norm.NFC.String(asked) == norm.NFC.String(onDisk) {
			return true
		}
		for _, name := range list(onDisk) {
			if name == onDisk || strings.HasPrefix(name, onDisk+"/") {
				return false
			}
		}
		return slices.Contains(list(p), p)
	}
}

// needsLook reports whether the entry c of dir, which a lookup found as
// fi, may be there under another spelling: its name has a letter whose
// other case the volume finds as the same file, or is not ASCII, which
// a volume may hold in another Unicode form.
func needsLook(dir, c string, fi os.FileInfo) bool {
	if !isASCII(c) {
		return true
	}
	other := swapCase(c)
	if other == c {
		return false // no letter: no case to differ in
	}
	o, err := os.Lstat(filepath.Join(dir, other))
	return err == nil && os.SameFile(fi, o)
}

func swapCase(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case 'a' <= r && r <= 'z':
			return r - 'a' + 'A'
		case 'A' <= r && r <= 'Z':
			return r - 'A' + 'a'
		}
		return r
	}, s)
}

// entries are the names in dir as the disk holds them.
func entries(dir string) map[string]bool {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	list, _ := f.Readdirnames(-1)
	out := make(map[string]bool, len(list))
	for _, n := range list {
		out[n] = true
	}
	return out
}

// spelledOnDisk is the name the entry of dir found as c, fi, has on disk.
func spelledOnDisk(dir, c string, fi os.FileInfo, in map[string]bool) string {
	for n := range in {
		if strings.EqualFold(n, c) || n != c && !isASCII(n) {
			if o, err := os.Lstat(filepath.Join(dir, n)); err == nil && os.SameFile(fi, o) {
				return n
			}
		}
	}
	return c
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
