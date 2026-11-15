package write

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// A pull request's head and base come into refs of their own; the
// remote's branches stay where they were.
func TestFetchPull(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	host := gittest.Init(t)
	host.Write("a", "base")
	base := host.Commit("base")
	host.Git("checkout", "-q", "-b", "topic")
	host.Write("a", "head")
	head := host.Commit("head")
	host.Git("update-ref", "refs/pull/3/head", head)
	host.Git("checkout", "-q", "main")
	h.r.Git("remote", "add", "origin", host.Dir)

	op, err := FetchPull(h.w, "origin", 3, "refs/pull/3/head", "main")
	h.do(op, err, true)
	if got := h.rev(PullHead(3)); got != head {
		t.Errorf("head %s, want %s", got, head)
	}
	if got := h.rev(PullBase(3)); got != base {
		t.Errorf("base %s, want %s", got, base)
	}
	if refs := h.r.Git("for-each-ref", "refs/remotes/"); strings.TrimSpace(refs) != "" {
		t.Errorf("remote branches moved: %s", refs)
	}

	for _, bad := range [][3]string{{"-x", "refs/pull/3/head", "main"}, {"origin", "pull/3/head", "main"},
		{"origin", "refs/pull/3/head:refs/heads/main", "main"}, {"origin", "refs/pull/3/head", "--upload-pack=x"}, {"origin", "refs/pull/3/head", "a:b"}} {
		if _, err := FetchPull(h.w, bad[0], 3, bad[1], bad[2]); err == nil {
			t.Errorf("fetched %q", bad)
		}
	}
}
