package statefile

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StaleAfter is how old a temporary file of WriteFile's must be for
// RemoveStale to take it. A write fills its temporary file and renames it
// within moments; one left an hour is of a process that ended before its
// rename (a quit that gave up on a volume that did not answer, a crash),
// or of one still stuck on such a volume, whose rename then fails and
// leaves the file as it was. A running copy of the app is never past its
// write's first moments otherwise.
const StaleAfter = time.Hour

// RemoveStale removes the temporary files a WriteFile of path left beside
// the file it writes (through a link, beside the file it leads to), when
// older than StaleAfter: ".<name>.<digits>", and those of the copies kept
// aside, ".<name>.broken.<digits>" and ".<name>.broken-<n>.<digits>".
// Those of a write running are younger. The folder may be the user's, so
// no other name is taken.
func RemoveStale(path string) {
	to, err := target(path)
	if err != nil {
		return
	}
	base := filepath.Base(to)
	removeStale(filepath.Dir(to), func(name string) bool { return ownTemp(name, base) })
}

// ownTemp reports whether name is one WriteFile gives its temporary file
// when writing base or a copy of it kept aside (asideName).
func ownTemp(name, base string) bool {
	rest, ok := strings.CutPrefix(name, "."+base+".")
	if !ok {
		return false
	}
	for n := range maxAside {
		if r, ok := strings.CutPrefix(rest, strings.TrimPrefix(asideName(base, n), base+".")+"."); ok {
			rest = r
			break
		}
	}
	return digits(rest)
}

// RemoveStaleIn is RemoveStale for every file of a folder the app alone
// writes, as the drafts'.
func RemoveStaleIn(dir string) {
	removeStale(dir, func(name string) bool { return strings.HasPrefix(name, ".") })
}

func removeStale(dir string, ours func(string) bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !ours(name) || !isTemp(name) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > StaleAfter {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

// isTemp reports whether name is one os.CreateTemp gave for the pattern
// ".<name>.*": a dot, then digits to its end.
func isTemp(name string) bool {
	i := strings.LastIndexByte(name, '.')
	return i > 0 && digits(name[i+1:])
}

// digits reports whether s is one or more decimal digits, what
// os.CreateTemp puts for its pattern's "*".
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
