package history

import (
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/commits"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

// Each filter keeps what git log keeps, in its order; a filter that keeps
// the graph hands over parents rewritten to the commits kept.
func TestFiltered(t *testing.T) {
	r := gittest.Init(t)
	base := r.Env
	as := func(day int, who string) {
		d := time.Date(2024, 1, day, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
		r.Env = append(append([]string{}, base...), "GIT_AUTHOR_DATE="+d, "GIT_COMMITTER_DATE="+d, "GIT_AUTHOR_NAME="+who)
	}
	commit := func(day int, file, text, msg, who string) {
		r.Write(file, text)
		r.Git("add", file)
		as(day, who)
		r.Git("commit", "-q", "-m", msg)
	}
	commit(1, "src/a.go", "package a\n", "add a", "Ada")
	commit(2, "docs/x.md", "x\n", "docs [x]", "Bob")
	r.Git("switch", "-q", "-c", "side")
	commit(3, "src/a.go", "package a\n// needle\n", "Fix Needle in a", "Bob")
	r.Git("switch", "-q", "main")
	commit(4, "docs/x.md", "x\ny\n", "more docs", "Ada")
	as(5, "Ada")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	r.Env = base
	head, _ := hex.DecodeString(strings.TrimSpace(r.Git("rev-parse", "HEAD")))
	run := testrun.Runner(t)

	check := func(name string, f Filter, gitArgs ...string) int {
		t.Helper()
		s := commits.New(20)
		var got []string
		edges := 0
		err := Filtered(t.Context(), run, r.GitDir(), [][]byte{head}, f, s, func(id uint32, parents []uint32) error {
			got = append(got, hex.EncodeToString(s.Snapshot().OID(id)))
			edges += len(parents)
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := strings.Fields(r.Git(append([]string{"log", "--format=%H"}, gitArgs...)...))
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %d commits %v, git log %v", name, len(got), short(got), short(want))
		}
		if !f.KeepsGraph() && edges > 0 {
			t.Errorf("%s: %d edges for a filter that picks commits out", name, edges)
		}
		t.Logf("%s: %d commits, %d edges", name, len(got), edges)
		return edges
	}
	check("all", Filter{}, "--topo-order")
	check("path", Filter{Paths: []string{"src/a.go"}}, "--topo-order", "--", "src/a.go")
	if e := check("first parent", Filter{FirstParent: true}, "--topo-order", "--first-parent"); e != 3 {
		t.Errorf("first parent: %d edges, want 3 along one line", e)
	}
	check("dates", Filter{Since: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), Until: time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)},
		"--topo-order", "--since=2024-01-02T00:00:00Z", "--until=2024-01-04T00:00:00Z")
	check("author", Filter{Author: "ada"}, "-i", "--author=ada")
	check("message", Filter{Message: "needle"}, "-i", "--grep=needle")
	check("changed text", Filter{Changed: "needle"}, "-Sneedle")
	check("changed regexp", Filter{Changed: "nee+dle", Regexp: true}, "-Gnee+dle")
	check("no merges", Filter{NoMerges: true}, "--no-merges")
	// A pattern is text: "[x]" matches only the brackets, not "Fix".
	check("literal", Filter{Message: "[x]"}, "-i", "--fixed-strings", "--grep=[x]")
}

func short(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id[:7]
	}
	return out
}
