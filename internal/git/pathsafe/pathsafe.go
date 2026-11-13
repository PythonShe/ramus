// Package pathsafe flags paths that are not what they look like, as git's
// own checks do (verify_path, is_ntfs_dotgit, is_hfs_dotgit), and guards
// the file operations the app does itself in a work tree.
package pathsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Flag is a reason a path is shown flagged.
type Flag uint8

const (
	// DotDot is a path with a "." or ".." component.
	DotDot Flag = 1 << iota
	// DotGit is a component that is .git, or that NTFS or HFS+ would open
	// as .git (".GIT", ".git.", "git~1", ".g\u200cit").
	DotGit
	// LineBreak is a path with a line or paragraph separator, which can
	// make one path read as two in a list or a log.
	LineBreak
	// Absolute is a path that starts at a root or a drive.
	Absolute
)

// Check returns the flags of a slash-separated path from git. A flagged
// path is shown with its flag, never hidden, and the app never follows
// it.
func Check(path string) Flag {
	var f Flag
	if strings.ContainsAny(path, "\n\r\u0085\u2028\u2029") {
		f |= LineBreak
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") || (len(path) >= 2 && path[1] == ':') {
		f |= Absolute
	}
	for _, c := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if c == "." || c == ".." {
			f |= DotDot
		}
		if IsDotGit(c) {
			f |= DotGit
		}
	}
	return f
}

// IsDotGit reports a path component that is .git or that a case-folding
// or name-mangling file system would open as .git.
func IsDotGit(name string) bool {
	return isNTFSDotGit(name) || isHFSDotGit(name)
}

// isNTFSDotGit follows git's is_ntfs_dotgit: ".git" in any case, then
// only spaces and dots (which NTFS drops), or an alternate stream; or the
// 8.3 short name "git~1".
func isNTFSDotGit(name string) bool {
	rest := ""
	switch {
	case len(name) >= 4 && strings.EqualFold(name[:4], ".git"):
		rest = name[4:]
	case len(name) >= 5 && strings.EqualFold(name[:5], "git~1"):
		rest = name[5:]
	default:
		return false
	}
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case ' ', '.':
		case ':', '\\', '/':
			return true
		default:
			return false
		}
	}
	return true
}

// isHFSDotGit follows git's is_hfs_dotgit: HFS+ ignores some code points
// and folds case, so ".g\u200dIT" opens .git.
func isHFSDotGit(name string) bool {
	var b strings.Builder
	for _, r := range name {
		if ignorableHFS(r) {
			continue
		}
		if r >= utf8.RuneSelf {
			return false
		}
		b.WriteRune(r)
	}
	return strings.EqualFold(b.String(), ".git")
}

// ignorableHFS lists the code points HFS+ drops from names.
func ignorableHFS(r rune) bool {
	switch {
	case r >= 0x200c && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x206a && r <= 0x206f, r == 0xfeff:
		return true
	}
	return false
}

// ErrUnsafe is returned for a path the app will not touch itself.
var ErrUnsafe = errors.New("pathsafe: path is not safe to touch")

// InWorkTree checks a slash-separated path before the app itself creates,
// writes or deletes it in the work tree at root (writing .gitignore,
// removing an untracked file): it must be relative, with no ".", ".." or
// .git component and no line break, and no directory on the way may be a
// symlink that leads out of the work tree. It returns the path on disk.
func InWorkTree(root, rel string) (string, error) {
	if rel == "" || Check(rel) != 0 {
		return "", fmt.Errorf("%w: %q", ErrUnsafe, rel)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	parts := strings.Split(rel, "/")
	cur := realRoot
	for i, p := range parts {
		next := filepath.Join(cur, p)
		st, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			// Nothing on disk from here on can be a link.
			return filepath.Join(append([]string{cur}, parts[i:]...)...), nil
		}
		if err != nil {
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 && i < len(parts)-1 {
			target, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf("%w: %q: %v", ErrUnsafe, rel, err)
			}
			if !within(realRoot, target) {
				return "", fmt.Errorf("%w: %q leads out of the work tree", ErrUnsafe, rel)
			}
			next = target
		}
		cur = next
	}
	// The last component itself may be a symlink: the operation acts on
	// the link, not on what it points to.
	return cur, nil
}

// Target checks a path the app hands to another program to open (an
// editor, the file manager, a user's action): InWorkTree, and when the
// path is itself a symlink, what it points to must be in the work tree
// too, since the program follows it.
func Target(root, rel string) (string, error) {
	p, err := InWorkTree(root, rel)
	if err != nil {
		return "", err
	}
	st, err := os.Lstat(p)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		return p, nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %v", ErrUnsafe, rel, err)
	}
	if !within(realRoot, target) {
		return "", fmt.Errorf("%w: %q leads out of the work tree", ErrUnsafe, rel)
	}
	return p, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Unquote reads a path as git writes one where it cannot use -z: as it is,
// or in double quotes with C escapes (\t, \n, \", \\ and octal bytes)
// when it holds anything unusual. A quoted path that does not parse is
// returned as it came.
func Unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	// git's escapes are a subset of Go's, with the same meaning.
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}
