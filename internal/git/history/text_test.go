package history

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// A row's text holds its subject and author, and for a merge the parents
// it merges, as git log's %p abbreviates them to seven digits.
func TestLoadTextMerged(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1\n")
	r.Commit("one")
	r.Git("checkout", "-q", "-b", "side")
	r.Write("b", "2\n")
	r.Commit("two")
	r.Git("checkout", "-q", "main")
	r.Write("c", "3\n")
	r.Commit("three")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	var ids [][]byte
	var want []string
	for _, line := range strings.Split(strings.TrimSpace(r.Git("log", "--format=%H %P")), "\n") {
		f := strings.Fields(line)
		id, _ := hex.DecodeString(f[0])
		ids = append(ids, id)
		var short []string
		for _, p := range f[1:] {
			short = append(short, p[:7])
		}
		if len(short) < 2 {
			short = nil
		}
		want = append(want, strings.Join(short, " and "))
	}
	p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := LoadText(p, func(id uint32) []byte { return ids[id] }, 0, len(ids), func(row int) uint32 { return uint32(row) })
	if err != nil {
		t.Fatal(err)
	}
	for i := range ids {
		subject, _, _ := c.Row(i)
		if got := c.Merged(i); got != want[i] {
			t.Errorf("row %d (%s): merged %q, want %q", i, subject, got, want[i])
		}
	}
	if s, a, _ := c.Row(0); s != "merge side" || a == "" {
		t.Fatalf("row 0: %q by %q", s, a)
	}
}
