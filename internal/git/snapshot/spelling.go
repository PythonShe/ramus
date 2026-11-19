package snapshot

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
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
// work tree at root holds under another spelling. Whether a folder's
// volume tells spellings apart is asked of the volume, by looking the
// name up as another case, not assumed from the system.
func checkSpelling(root string, paths []string) error {
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
					return &SpellingError{Path: p, OnDisk: path.Join(append([]string{rel, spelledOnDisk(dir, c, fi, in)}, parts[i+1:]...)...)}
				}
			}
			dir, rel = next, path.Join(rel, c)
		}
	}
	return nil
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
