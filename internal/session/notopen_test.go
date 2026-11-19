package session

import (
	"context"
	"strings"
	"testing"

	"ramus/internal/forge"
	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/history"
	"ramus/internal/git/lfs"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/signature"
	"ramus/internal/git/snapshot"
	"ramus/internal/git/write"
)

// A read asked while no repository is open, as while it opens again
// after Trust, is answered all the same: a view that marked it asked
// would otherwise wait on it for good.
func TestReadsWithNoRepositoryAreAnswered(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	head := mustHex(t, strings.TrimSpace(r.Commit("one")))
	m := make(mainThread, 256)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	m.until(t, "the status", func() bool { return s.State.Status != nil && s.State.Refs != nil })
	s.Close()
	<-s.Done()
	if s.Repo != nil {
		t.Fatal("a closed session keeps its repository")
	}
	find := func(forge.Kind) (*forge.CLI, error) { return nil, nil }
	reads := map[string]func(answer func()){
		"LoadAuthors":   func(a func()) { s.LoadAuthors(func([]Person) { a() }) },
		"LoadBisect":    func(a func()) { s.LoadBisect(func(BisectState, error) { a() }) },
		"Blame":         func(a func()) { s.Blame("a.txt", func([]byte, error) {}, func(BlameUpdate) { a() }) },
		"LoadRefInfo":   func(a func()) { s.LoadRefInfo(func(RefInfo) { a() }) },
		"Patch":         func(a func()) { s.Patch(diff.PatchRequest{}, Sides{}, func(PatchResult) { a() }) },
		"ListClean":     func(a func()) { s.ListClean(false, func([]string, []write.FileState, error) { a() }) },
		"HeadMessage":   func(a func()) { s.HeadMessage(func(string, error) { a() }) },
		"CheckLineEnds": func(a func()) { s.CheckLineEnds(func(LineEnds) { a() }) },
		"LoadPrepared":  func(a func()) { s.LoadPrepared("#", func(Prepared) { a() }) },
		"Verify":        func(a func()) { s.Verify("", "refs/tags/v1", func(signature.Status, error) { a() }) },
		"FindCleanup":   func(a func()) { s.FindCleanup("refs/heads/main", "", nil, func([]Candidate, error) { a() }) },
		"PreviewMerge":  func(a func()) { s.PreviewMerge("main", func(MergePreview, error) { a() }) },
		"LoadConflict":  func(a func()) { s.LoadConflict("a.txt", func(ConflictFile) { a() }) },
		"Compare":       func(a func()) { s.Compare(head, head, func(CommitDetail) { a() }) },
		"CompareWorkTree": func(a func()) {
			s.CompareWorkTree(head, func(CommitDetail) { a() })
		},
		"WorkTreeComparePatch": func(a func()) {
			s.WorkTreeComparePatch(new(PatchSlot), head, []string{"a.txt"}, 0, 0, Sides{}, func(PatchResult) { a() })
		},
		"EditRange": func(a func()) {
			s.EditRange(head, func(write.Rewrite, write.Seen, []EditCommit, error) { a() })
		},
		"OpenDiffTool":  func(a func()) { s.OpenDiffTool(DiffToolRequest{}, func(error) { a() }) },
		"CountChanges":  func(a func()) { s.CountChanges(nil, nil, func(ChangeLines) { a() }) },
		"LFSLocks":      func(a func()) { s.LFSLocks(func(lfs.Locks, error) { a() }) },
		"HistoryText":   func(a func()) { s.HistoryText([][]byte{head}, func(*history.TextChunk, int, error) { a() }) },
		"LoadCommit":    func(a func()) { s.LoadCommit(head, func(CommitDetail) { a() }) },
		"CommitPatch":   func(a func()) { s.CommitPatch(CommitDetail{OID: head}, nil, 0, 0, Sides{}, func(PatchResult) { a() }) },
		"HostPatch":     func(a func()) { s.HostPatch(CommitDetail{OID: head}, nil, 0, Sides{}, func(PatchResult) { a() }) },
		"ResolveCommit": func(a func()) { s.ResolveCommit("HEAD", func([]byte, error) { a() }) },
		"FindRow":       func(a func()) { s.FindRow(HistoryState{}, head, func(int) { a() }) },
		"FormatPatch":   func(a func()) { s.FormatPatch(head, func(string, error) { a() }) },
		"FileLog":       func(a func()) { s.FileLog("a.txt", FileLogOptions{}, func([]FileCommit, bool, error) { a() }) },
		"LineLog":       func(a func()) { s.LineLog("a.txt", 1, 1, func(*LineLog, error) { a() }) },
		"GitCommands":   func(a func()) { s.GitCommands(func([]run.Record) { a() }) },
		"SavePatch":     func(a func()) { s.SavePatch(PatchSave{}, "x.patch", func(error) { a() }) },
		"ReadPatchFile": func(a func()) { s.ReadPatchFile("x.patch", func(PatchFile, error) { a() }) },
		"LoadPulls":     func(a func()) { s.LoadPulls(find, &RefInfo{}, func(PullsResult) { a() }) },
		"LoadCommitPull": func(a func()) {
			s.LoadCommitPull(find, head, "one", func(CommitPullResult) { a() })
		},
		"Reflog":        func(a func()) { s.Reflog("HEAD", func([]ReflogEntry, error) { a() }) },
		"LostCommits":   func(a func()) { s.LostCommits(func([]LostCommit, error) { a() }) },
		"Snapshots":     func(a func()) { s.Snapshots(func([]snapshot.Snapshot, error) { a() }) },
		"SnapshotFiles": func(a func()) { s.SnapshotFiles(snapshot.Snapshot{}, func([]snapshot.File, error) { a() }) },
		"LoadCommitRelations": func(a func()) {
			s.LoadCommitRelations(head, func(CommitRelations) { a() })
		},
		"LoadStats":  func(a func()) { s.LoadStats(StatsRequest{}, func(StatsResult) { a() }) },
		"Then":       func(a func()) { s.Then(nil, func(error) { a() }) },
		"OpenPulls":  func(a func()) { s.OpenPulls(find, func(OpenPullsResult) { a() }) },
		"PullReview": func(a func()) { s.PullReview(find, forge.Repo{}, 1, func(forge.Review, error) { a() }) },
		"ForgeDo": func(a func()) {
			s.ForgeDo(find, forge.Repo{}, func(context.Context, *forge.CLI) error { return nil }, func(error) { a() })
		},
		"MergeBase":     func(a func()) { s.MergeBase(head, head, func([]byte, error) { a() }) },
		"LoadPullRange": func(a func()) { s.LoadPullRange(1, func(PullRange, error) { a() }) },
		"LoadTrack":     func(a func()) { s.LoadTrack(func(Track) { a() }) },
		"LoadSubjects":  func(a func()) { s.LoadSubjects([][]byte{head}, func([]string) { a() }) },
		"FileSizes":     func(a func()) { s.FileSizes("HEAD", func(PathWeights) { a() }) },
		"ChangeCounts":  func(a func()) { s.ChangeCounts(StatsRequest{}, func(PathWeights) { a() }) },
		"LoadSubmoduleRange": func(a func()) {
			s.LoadSubmoduleRange("sub", head, head, func(SubmoduleRange) { a() })
		},
		"SummarizeWorktrees": func(a func()) {
			s.SummarizeWorktrees([]repo.Worktree{{Path: r.Dir}}, func(WorktreeSummary) { a() })
		},
		"WatchWorktrees": func(a func()) {
			s.WatchWorktrees([]repo.Worktree{{Path: r.Dir}}, func(WorktreeSummary) { a() })
		},
		"WorktreePatch": func(a func()) { s.WorktreePatch(r.Dir, "a.txt", false, func(PatchResult) { a() }) },
	}
	for name, read := range reads {
		calls := 0
		read(func() { calls++ })
		// Some answer on the main thread: what was posted is run.
		for len(m) > 0 {
			(<-m)()
		}
		if calls != 1 {
			t.Errorf("%s: answered %d times with no repository open, want once", name, calls)
		}
	}
}
