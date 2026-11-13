package session

import (
	"bytes"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func weights(pw PathWeights) map[string]int64 {
	out := map[string]int64{}
	i := 0
	for p := range bytes.SplitSeq(bytes.TrimSuffix(pw.Paths, []byte{0}), []byte{0}) {
		out[string(p)] = pw.Weights[i]
		i++
	}
	return out
}

func TestTreemapWeights(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a.b", "12345")
	r.Write("a/x", "1")
	r.Write("a/y z", "22")
	r.Commit("one")
	r.Write("a/x", "11")
	r.Commit("two")
	r.Write("a/x", "111")
	r.Write("a.b", "1")
	r.Commit("three")
	r.Git("rm", "-q", "a/y z")
	r.Commit("four")

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "refs", func() bool { return s.State.Refs != nil })

	var sizes, changes *PathWeights
	s.FileSizes("", func(pw PathWeights) { sizes = &pw })
	s.ChangeCounts(StatsRequest{}, func(pw PathWeights) { changes = &pw })
	m.until(t, "both", func() bool { return sizes != nil && changes != nil })
	if sizes.Err != nil || changes.Err != nil {
		t.Fatal(sizes.Err, changes.Err)
	}
	if got := weights(*sizes); len(got) != 2 || got["a.b"] != 1 || got["a/x"] != 3 {
		t.Fatalf("sizes %v", got)
	}
	// Byte order: "a.b" before "a/x".
	if !strings.HasPrefix(string(sizes.Paths), "a.b\x00") {
		t.Fatalf("order %q", sizes.Paths)
	}
	if got := weights(*changes); got["a/x"] != 3 || got["a.b"] != 2 || got["a/y z"] != 2 || len(got) != 3 || changes.Commits != 4 {
		t.Fatalf("changes %v over %d commits", got, changes.Commits)
	}
}
