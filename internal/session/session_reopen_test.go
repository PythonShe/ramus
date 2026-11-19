package session

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/history"
	"ramus/internal/git/run"
	"ramus/internal/git/signature"
)

// What a read finds of a repository that closes and opens again before
// the main thread takes it, as when Trust opens it again, is not handed
// over: it may point into the closed repository's mappings. A view
// waiting on the answer hears that the repository opened again; one
// that does not wait hears nothing.
func TestReadsOfAClosedRepositoryAreNotHandedOver(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	parent := mustHex(t, strings.TrimSpace(r.Commit("one")))
	r.Write("a.txt", "two\n")
	head := mustHex(t, strings.TrimSpace(r.Commit("two")))
	r.Git("commit-graph", "write", "--reachable")
	r.Write("a.txt", "three\n")
	r.Git("add", "a.txt")

	// outcome is the last answer, reopened when it is the one a read of a
	// repository opened again gives.
	type outcome struct {
		called, reopened bool
	}
	cases := []struct {
		name string
		// waits is set for a read whose view waits on its answer.
		waits bool
		start func(s *Session, answer func(reopened bool))
	}{
		{"HistoryText", true, func(s *Session, answer func(bool)) {
			s.HistoryText([][]byte{head}, func(c *history.TextChunk, _ int, err error) { answer(c == nil && err == nil) })
		}},
		{"LoadCommit", true, func(s *Session, answer func(bool)) {
			s.LoadCommit(head, func(d CommitDetail) { answer(errors.Is(d.Err, ErrReopened) && d.Done) })
		}},
		{"CommitPatch", true, func(s *Session, answer func(bool)) {
			s.CommitPatch(CommitDetail{OID: head, Parents: [][]byte{parent}}, nil, 0, 0, Sides{}, func(p PatchResult) { answer(errors.Is(p.Err, ErrReopened)) })
		}},
		{"Patch", true, func(s *Session, answer func(bool)) {
			s.Patch(diff.PatchRequest{Source: diff.Staged, Paths: []string{"a.txt"}, Context: -1}, Sides{}, func(p PatchResult) { answer(errors.Is(p.Err, ErrReopened)) })
		}},
		{"LoadCommitRelations", true, func(s *Session, answer func(bool)) {
			s.LoadCommitRelations(head, func(rel CommitRelations) { answer(errors.Is(rel.Err, ErrReopened)) })
		}},
		{"Verify", true, func(s *Session, answer func(bool)) {
			s.Verify(hex.EncodeToString(head), "", func(_ signature.Status, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"HeadMessage", true, func(s *Session, answer func(bool)) {
			s.HeadMessage(func(_ string, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"CheckLineEnds", true, func(s *Session, answer func(bool)) {
			s.CheckLineEnds(func(le LineEnds) { answer(errors.Is(le.Err, ErrReopened)) })
		}},
		{"LoadPrepared", true, func(s *Session, answer func(bool)) {
			s.LoadPrepared("#", func(Prepared) { answer(true) })
		}},
		{"GitCommands", true, func(s *Session, answer func(bool)) {
			s.GitCommands(func(recs []run.Record) { answer(recs == nil) })
		}},
		{"LoadRefInfo", true, func(s *Session, answer func(bool)) {
			s.LoadRefInfo(func(info RefInfo) { answer(errors.Is(info.Err, ErrReopened)) })
		}},
		{"FindCleanup", true, func(s *Session, answer func(bool)) {
			s.FindCleanup("refs/heads/main", "", []BranchInfo{{Name: "refs/heads/main"}}, func(_ []Candidate, err error) { answer(errors.Is(err, ErrReopened)) })
		}},
		{"LoadTrack", true, func(s *Session, answer func(bool)) {
			s.LoadTrack(func(tr Track) { answer(tr.Upstream == "") })
		}},
		{"ResolveCommit", false, func(s *Session, answer func(bool)) {
			s.ResolveCommit("HEAD", func([]byte, error) { answer(false) })
		}},
		{"FormatPatch", false, func(s *Session, answer func(bool)) {
			s.FormatPatch(head, func(string, error) { answer(false) })
		}},
		{"LoadSubjects", false, func(s *Session, answer func(bool)) {
			s.LoadSubjects([][]byte{head}, func([]string) { answer(false) })
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := make(mainThread, 256)
			s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
			defer func() { s.Close(); <-s.Done() }()
			m.until(t, "status", func() bool {
				return s.State.Status != nil && s.State.Refs != nil && s.refreshIdle.Load() && len(m) == 0
			})
			var got outcome
			calls := 0
			c.start(s, func(reopened bool) { calls++; got = outcome{true, reopened} })
			// On the main thread before anything the read hands over.
			s.reopenTrusted()
			if c.waits {
				m.until(t, "the answer", func() bool { return got.called })
				m.pump(100 * time.Millisecond)
				if !got.reopened || calls != 1 {
					t.Fatalf("answered %d times, the last %+v; want once, as reopened", calls, got)
				}
				return
			}
			m.pump(500 * time.Millisecond)
			if got.called {
				t.Fatal("an answer read of the closed repository was handed over")
			}
		})
	}
}
