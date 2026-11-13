// Package gitexe finds the git executable, reads its version, and says
// which features that version has.
package gitexe

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a git release, as git version prints it.
type Version struct {
	Major, Minor, Patch int
	// Raw is the whole version string, with any vendor suffix
	// ("2.39.5 (Apple Git-154)", "2.47.1.windows.1").
	Raw string
}

// ParseVersion reads the output of git version.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	raw := strings.TrimPrefix(s, "git version ")
	if raw == s {
		return Version{}, fmt.Errorf("gitexe: not a git version: %q", s)
	}
	v := Version{Raw: raw}
	f := strings.FieldsFunc(raw, func(r rune) bool { return r == '.' || r == ' ' || r == '-' })
	nums := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range nums {
		if i >= len(f) {
			break
		}
		n, err := strconv.Atoi(f[i])
		if err != nil {
			if i < 2 {
				return Version{}, fmt.Errorf("gitexe: bad version %q", raw)
			}
			break // "2.45.rc1" and the like
		}
		*p = n
	}
	return v, nil
}

// AtLeast reports whether v is the release major.minor.patch or later.
func (v Version) AtLeast(major, minor, patch int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	return v.Patch >= patch
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Feature is something Ramus uses only where git has it, with a fallback
// elsewhere.
type Feature int

const (
	// BatchCommand is cat-file --batch-command, for the object reader.
	// Fallback: --batch, one process per batch.
	BatchCommand Feature = iota
	// MergeTreeWriteTree is merge-tree --write-tree, for listing conflicts
	// before a merge. Fallback: no preview.
	MergeTreeWriteTree
	// NoLazyFetch is --no-lazy-fetch / GIT_NO_LAZY_FETCH, so a read never
	// fetches from a promisor remote. Fallback: partial clones are read
	// only through git and lazy fetches are reported.
	NoLazyFetch
	// History is git history reword. Fallback: an interactive rebase.
	History
	// HistoryFixup is git history fixup. Fallback: a commit --fixup and an
	// autosquash rebase.
	HistoryFixup
	// HistoryDrop is git history drop. Fallback: a rebase.
	HistoryDrop
	// HookList is git hook list, which sees hooks defined in config.
	// Fallback: before hooks could be defined in config, the hooks
	// directory is all there is, so it is listed.
	HookList
	// BranchDeleteMerged is branch --delete-merged. Fallback: Ramus lists
	// merged branches itself and deletes them with branch -d.
	BranchDeleteMerged
	// FSMonitorLinux is git's built-in fsmonitor daemon on Linux.
	// Fallback: no fsmonitor offer on Linux.
	FSMonitorLinux
	numFeatures
)

// since is the first release with each feature, from git's release notes.
var since = [numFeatures]struct {
	name                string
	major, minor, patch int
}{
	BatchCommand:       {"cat-file --batch-command", 2, 36, 0},
	MergeTreeWriteTree: {"merge-tree --write-tree", 2, 38, 0},
	NoLazyFetch:        {"--no-lazy-fetch", 2, 45, 0},
	History:            {"git history", 2, 54, 0},
	HistoryFixup:       {"git history fixup", 2, 55, 0},
	HistoryDrop:        {"git history drop", 2, 56, 0},
	HookList:           {"git hook list", 2, 54, 0},
	BranchDeleteMerged: {"branch --delete-merged", 2, 56, 0},
	FSMonitorLinux:     {"fsmonitor on Linux", 2, 55, 0},
}

// Has reports whether git v has feature f.
func (v Version) Has(f Feature) bool {
	s := since[f]
	return v.AtLeast(s.major, s.minor, s.patch)
}

// String names a feature.
func (f Feature) String() string { return since[f].name }

// Minimum is the oldest git Ramus runs: cat-file --batch-command is its
// object reader.
var Minimum = Version{Major: 2, Minor: 36, Raw: "2.36.0"}

// SecurityFixed is the newest git line with a security fix, as of the
// release of this Ramus (checked 2026-11). Older git gets a warning.
var SecurityFixed = Version{Major: 2, Minor: 50, Patch: 1, Raw: "2.50.1"}
