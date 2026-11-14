package session

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"ramus/internal/git/cache"
	"ramus/internal/git/diff"
	"ramus/internal/git/repo"
)

// answer is a patch or a commit read for a view, kept by what it was read
// from, so that choosing a file or a commit again, or a refresh that left
// it as it was, starts no process: on a network drive each one costs
// seconds (GitHub Desktop #21440). The answers live on the main thread,
// as the views do.
type answer struct {
	patch  *PatchResult
	detail *CommitDetail
}

// AnswersBudget bounds the answers a session keeps. With the history
// view's text budget, it is what an open repository holds beyond its
// structure (commits, lanes, refs, status), which is the size of the
// repository, not of how long it has been browsed.
const AnswersBudget = 64 << 20

// maxAnswer is the largest answer kept, so that one long patch does not
// push out every other.
const maxAnswer = AnswersBudget / 4

// SetAnswersBudget changes the bytes the session's answers may hold.
func (s *Session) SetAnswersBudget(n int) { s.answers().SetBudget(n) }

// racyWindow is how recently a work tree file may have changed and still
// have its patch kept: a write within the same timestamp tick as the
// read would go unseen otherwise, as git's racy index entries would.
const racyWindow = 2 * time.Second

func (s *Session) answers() *cache.Cache[string, answer] {
	if s.answerCache == nil {
		s.answerCache = cache.New[string, answer](AnswersBudget, answer.bytes)
	}
	return s.answerCache
}

// keep stores a, unless it is too large to be worth it.
func (s *Session) keep(key string, a answer) {
	c := s.answers()
	if n := a.bytes(); n <= maxAnswer && n <= c.Budget()/4 {
		c.Put(key, a)
	}
}

// DropAnswers forgets every patch and commit kept, for a refresh the
// user asks for: what git shows can change with no file the key covers
// changing, through config or .gitattributes.
func (s *Session) DropAnswers() {
	if s.answerCache != nil {
		s.answerCache.Clear()
	}
}

func (a answer) bytes() int {
	n := 64
	if p := a.patch; p != nil {
		n += len(p.Old) + len(p.New)
		if d := p.Parsed; d != nil {
			n += len(d.Text) + len(d.Files)*int(unsafe.Sizeof(diff.File{})) +
				len(d.Hunks)*int(unsafe.Sizeof(diff.Hunk{})) + len(d.Lines)*int(unsafe.Sizeof(diff.Line{}))
		}
	}
	if d := a.detail; d != nil {
		c := &d.Commit
		n += len(d.OID) + len(c.Message) + len(c.Author.Name) + len(c.Author.Email) +
			len(c.Committer.Name) + len(c.Committer.Email) + len(d.Signer) + 64*len(d.Parents)
		if d.Files != nil {
			n += d.Files.Bytes()
		}
	}
	return n
}

// fileKey tells a work tree file's content apart without reading it.
type fileKey struct {
	size, mtime int64
	id          uint64
	mode        os.FileMode
}

// readFileKey returns the key of the file at path, read at now; ok is
// false for a file whose patch is not to be kept: one that is not a
// regular file, or that changed within racyWindow of now.
func readFileKey(path string, now time.Time) (k fileKey, ok bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || !fi.ModTime().Before(now.Add(-racyWindow)) {
		return fileKey{}, false
	}
	return fileKey{size: fi.Size(), mtime: fi.ModTime().UnixNano(), id: repo.FileID(fi), mode: fi.Mode()}, true
}

const gitlink = 0o160000

// workPatchKey is what a work tree patch is kept by, "" when it is not
// kept; work is set when the work tree file's key completes it. A
// submodule's patch is never kept: it reads another repository.
func workPatchKey(req diff.PatchRequest, sd Sides) (key string, work bool) {
	if sd.Modes[0] == gitlink || sd.Modes[1] == gitlink || len(req.Paths) == 0 {
		return "", false
	}
	switch req.Source {
	case diff.Staged:
		if sd.Old == nil || sd.New == nil || sd.WorkTreePath != "" {
			return "", false
		}
	case diff.Unstaged:
		if sd.Old == nil || sd.WorkTreePath == "" {
			return "", false
		}
		work = true
	case diff.Untracked:
		if sd.WorkTreePath == "" {
			return "", false
		}
		work = true
	default:
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "w\x00%d\x00%d\x00%d\x00%t\x00%t\x00%d\x00%d\x00%x\x00%x\x00%o\x00%o\x00%s",
		req.Source, req.Context, req.MaxBytes, req.ForApply, req.Renames, req.Whitespace, sd.Max,
		sd.Old, sd.New, sd.Modes[0], sd.Modes[1], sd.WorkTreePath)
	for _, p := range req.Paths {
		b.WriteString("\x00" + p)
	}
	return b.String(), work
}

func (k fileKey) String() string {
	return fmt.Sprintf("\x00%d\x00%d\x00%d\x00%o", k.size, k.mtime, k.id, k.mode)
}

// commitPatchKey is what a commit's patch is kept by: all of it names
// objects, which do not change.
func commitPatchKey(parent, oid []byte, paths []string, maxBytes int, ws diff.Whitespace, sd Sides) string {
	var b strings.Builder
	fmt.Fprintf(&b, "c\x00%x\x00%x\x00%d\x00%d\x00%d\x00%x\x00%x", parent, oid, maxBytes, ws, sd.Max, sd.Old, sd.New)
	for _, p := range paths {
		b.WriteString("\x00" + p)
	}
	return b.String()
}

func commitKey(oid []byte) string { return "d\x00" + string(oid) }
