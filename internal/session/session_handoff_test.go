package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/lfs"
	"ramus/internal/git/repo"
	"ramus/internal/git/snapshot"
	"ramus/internal/git/write"
)

// drainClosed runs the main thread until every goroutine of old, a
// repository closed, has ended, and then what they handed it: all that a
// read of old will ever answer has been answered.
func (m mainThread) drainClosed(t *testing.T, old *repo.Repository) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case fn := <-m:
			fn()
		case <-old.Life().Done():
			for {
				select {
				case fn := <-m:
					fn()
				default:
					return
				}
			}
		case <-deadline:
			t.Fatal("the closed repository's work never ended")
		}
	}
}

// handOffCase is a read of the session and what its answer must be when
// its repository closed and opened again under it.
type handOffCase struct {
	name string
	// view is set for a read of the view shown, which leaving it stops.
	view  bool
	start func(s *Session, answer func(reopened bool))
}

func handOffCases(t *testing.T, r *gittest.Repo, parent, head []byte) []handOffCase {
	patchFile := filepath.Join(t.TempDir(), "a.patch")
	os.WriteFile(patchFile, []byte(r.Git("diff", "HEAD")), 0o644)
	return []handOffCase{
		{"LoadAuthors", false, func(s *Session, answer func(bool)) {
			s.LoadAuthors(func(p []Person) { answer(p == nil) })
		}},
		{"LoadBisect", false, func(s *Session, answer func(bool)) {
			s.LoadBisect(func(st BisectState, err error) { answer(!st.Active && st.Left == -1 && err == nil) })
		}},
		{"Blame", true, func(s *Session, answer func(bool)) {
			s.Blame("a.txt", func([]byte, error) { answer(false) }, func(u BlameUpdate) { answer(u.Done && errors.Is(u.Err, ErrReopened)) })
		}},
		{"ListClean", false, func(s *Session, answer func(bool)) {
			s.ListClean(false, func(_ []string, _ []write.FileState, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"Compare", true, func(s *Session, answer func(bool)) {
			s.Compare(parent, head, func(d CommitDetail) { answer(d.Done && errors.Is(d.Err, ErrReopened)) })
		}},
		{"CompareWorkTree", true, func(s *Session, answer func(bool)) {
			s.CompareWorkTree(head, func(d CommitDetail) { answer(d.Done && errors.Is(d.Err, ErrReopened)) })
		}},
		{"PreviewMerge", false, func(s *Session, answer func(bool)) {
			s.PreviewMerge("main", func(_ MergePreview, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"LoadConflict", true, func(s *Session, answer func(bool)) {
			s.LoadConflict("a.txt", func(cf ConflictFile) { answer(errors.Is(cf.Err, ErrReopened)) })
		}},
		{"EditRange", true, func(s *Session, answer func(bool)) {
			s.EditRange(parent, func(_ write.Rewrite, _ write.Seen, _ []EditCommit, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"FileLog", true, func(s *Session, answer func(bool)) {
			s.FileLog("a.txt", FileLogOptions{}, func(_ []FileCommit, _ bool, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"LineLog", true, func(s *Session, answer func(bool)) {
			s.LineLog("a.txt", 1, 1, func(_ *LineLog, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"LFSLocks", true, func(s *Session, answer func(bool)) {
			s.LFSLocks(func(_ lfs.Locks, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"Snapshots", false, func(s *Session, answer func(bool)) {
			s.Snapshots(func(_ []snapshot.Snapshot, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"SnapshotFiles", false, func(s *Session, answer func(bool)) {
			s.SnapshotFiles(snapshot.Snapshot{}, func(_ []snapshot.File, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"Reflog", false, func(s *Session, answer func(bool)) {
			s.Reflog("HEAD", func(_ []ReflogEntry, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"LostCommits", true, func(s *Session, answer func(bool)) {
			s.LostCommits(func(_ []LostCommit, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"MergeBase", true, func(s *Session, answer func(bool)) {
			s.MergeBase(parent, head, func(_ []byte, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"LoadPullRange", false, func(s *Session, answer func(bool)) {
			s.LoadPullRange(1, func(_ PullRange, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"LoadStats", true, func(s *Session, answer func(bool)) {
			s.LoadStats(StatsRequest{}, func(res StatsResult) { answer(res.Done && errors.Is(res.Err, ErrReopened)) })
		}},
		{"FileSizes", true, func(s *Session, answer func(bool)) {
			s.FileSizes("HEAD", func(pw PathWeights) { answer(errors.Is(pw.Err, ErrReopened)) })
		}},
		{"ChangeCounts", true, func(s *Session, answer func(bool)) {
			s.ChangeCounts(StatsRequest{}, func(pw PathWeights) { answer(errors.Is(pw.Err, ErrReopened)) })
		}},
		{"LoadSubmoduleRange", false, func(s *Session, answer func(bool)) {
			s.LoadSubmoduleRange("sub", parent, head, func(sr SubmoduleRange) { answer(errors.Is(sr.Err, ErrReopened)) })
		}},
		{"SummarizeWorktrees", false, func(s *Session, answer func(bool)) {
			s.SummarizeWorktrees([]repo.Worktree{{Path: r.Dir}}, func(sum WorktreeSummary) { answer(sum.Path == r.Dir && errors.Is(sum.Err, ErrReopened)) })
		}},
		{"WorktreePatch", false, func(s *Session, answer func(bool)) {
			s.WorktreePatch(r.Dir, "a.txt", false, func(p PatchResult) { answer(errors.Is(p.Err, ErrReopened)) })
		}},
		{"ReadPatchFile", false, func(s *Session, answer func(bool)) {
			s.ReadPatchFile(patchFile, func(_ PatchFile, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"ResolveCommit", false, func(s *Session, answer func(bool)) {
			s.ResolveCommit("HEAD", func(_ []byte, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"FormatPatch", false, func(s *Session, answer func(bool)) {
			s.FormatPatch(head, func(_ string, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"ReadSubjects", false, func(s *Session, answer func(bool)) {
			s.ReadSubjects([][]byte{head}, func(_ []string, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"SavePatch", false, func(s *Session, answer func(bool)) {
			// The file is written, or not, whichever repository is open:
			// what it says, the write's own end or ErrReopened, is the
			// answer either way.
			out := filepath.Join(t.TempDir(), "out.patch")
			s.SavePatch(PatchSave{Paths: []string{"a.txt"}}, out, func(error) { answer(true) })
		}},
	}
}

// A read whose repository closes and opens again under it, as Trust does,
// is answered once, as reopened, or as having nothing, and with nothing
// read of the repository closed; whether it was cut short or had read all
// before. A read of a session closed is not answered at all.
func TestHandOffsOfAClosedRepository(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	parent := mustHex(t, strings.TrimSpace(r.Commit("one")))
	r.Write("a.txt", "two\n")
	head := mustHex(t, strings.TrimSpace(r.Commit("two")))
	r.Write("a.txt", "three\n")
	cases := handOffCases(t, r, parent, head)

	t.Run("reopened", func(t *testing.T) {
		s, m := openSettled(t, r)
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				var got []bool
				c.start(s, func(reopened bool) { got = append(got, reopened) })
				old, writes := s.Repo, s.Writes
				// On the main thread before anything the read hands over.
				s.reopenTrusted()
				m.drainClosed(t, old)
				if len(got) != 1 || !got[0] {
					t.Errorf("answered %v; want once, as reopened", got)
				}
				if s.Writes == writes {
					t.Error("the views that read again after a write do not after the repository opened again")
				}
				if s.Repo == old || s.authors.r == old {
					t.Error("what was read of the repository closed was kept")
				}
				m.until(t, "the repository open again", func() bool { return s.State.Status != nil && s.State.Refs != nil })
				m.settle(t, s)
			})
		}
	})

	// A read of a view left is not answered: nothing waits on it, and an
	// answer would stand for a read of the view shown now.
	t.Run("of a view left", func(t *testing.T) {
		s, m := openSettled(t, r)
		calls := map[string]int{}
		for _, c := range cases {
			if !c.view {
				continue
			}
			s.SetView("one")
			c.start(s, func(bool) { calls[c.name]++ })
			s.SetView("two")
		}
		old := s.Repo
		s.reopenTrusted()
		m.drainClosed(t, old)
		if len(calls) != 0 {
			t.Fatalf("reads of a view left were answered: %v", calls)
		}
	})

	t.Run("closed", func(t *testing.T) {
		s, m := openSettled(t, r)
		calls := map[string]int{}
		for _, c := range cases {
			c.start(s, func(bool) { calls[c.name]++ })
		}
		old := s.Repo
		s.Close()
		m.drainClosed(t, old)
		if len(calls) != 0 {
			t.Fatalf("reads of a session closed were answered: %v", calls)
		}
	})
}
