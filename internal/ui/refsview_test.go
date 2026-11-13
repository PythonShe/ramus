package ui

import (
	"path/filepath"
	"strings"
	"testing"
)

// The branches view groups by "/", shows each branch against its
// upstream, and runs what its menus offer through git.
func TestRefsView(t *testing.T) {
	h := newHarness(t)
	up := h.repoWith()
	r := h.repoWith()
	r.Git("remote", "add", "origin", up.Dir)
	r.Git("fetch", "-q", "origin")
	r.Git("branch", "--set-upstream-to", "origin/main")
	r.Write("b", "2")
	r.Commit("ahead of origin")
	r.Git("branch", "feature/alpha")
	r.Git("branch", "feature/beta")
	r.Git("branch", "merged-one", "HEAD~1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	h.click("Branches and Tags")
	h.until("the branches", h.shows("origin/main ↑1"))
	tb := h.app.current()
	if !hasExact(h, "alpha") || !hasExact(h, "feature/") {
		t.Fatalf("not grouped: %q", h.tt.Texts())
	}
	rev := func(x string) string {
		out, _ := r.Run(nil, "rev-parse", "-q", "--verify", x)
		return strings.TrimSpace(out)
	}
	waitIdle := func(what string) {
		h.until(what, func() bool {
			l := tb.s.Log
			return len(l) > 0 && !l[len(l)-1].Running
		})
	}

	// A filter keeps what matches.
	h.click("Filter")
	h.tt.Type("bet")
	h.until("the filter", func() bool { return hasExact(h, "beta") && !hasExact(h, "alpha") })
	tb.refs.filter = ""

	// Checking out over local changes asks, and stashing takes them.
	r.Write("README", "changed")
	h.until("the change", func() bool { st := tb.s.State.Status; return st != nil && st.Len() > 0 })
	h.until("alpha", func() bool { return hasExact(h, "alpha") })
	if err := h.tt.RightClick("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Check Out"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", h.shows("Stash Them"))
	h.click("Stash Them")
	h.until("the switch", func() bool { return tb.s.State.Head.Target == "refs/heads/feature/alpha" })
	if !strings.Contains(r.Git("stash", "list"), "before switching to feature/alpha") {
		t.Fatal("not stashed")
	}

	// Rename through the dialog.
	if err := h.tt.RightClick("beta"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Rename…"); err != nil {
		t.Fatal(err)
	}
	h.until("the dialog", h.shows("Rename feature/beta"))
	tb.refs.dlg.values[0] = "feature/gamma"
	h.click("Rename")
	h.until("the rename", func() bool { return rev("refs/heads/feature/gamma") != "" && rev("refs/heads/feature/beta") == "" })

	// Clean up finds the merged branch, against main, and deletes it.
	h.until("the rename shown", func() bool { return hasExact(h, "gamma") })
	h.click("Clean Up…")
	// feature/gamma is at main's tip, so merged too; it is kept.
	h.until("the candidates", h.shows("Delete 2 Branches"))
	for i, f := range tb.refs.clean.found {
		if f.Branch.Name == "refs/heads/feature/gamma" {
			tb.refs.clean.picked[i] = false
		}
	}
	h.until("one picked", h.shows("Delete 1 Branch"))
	h.click("Delete 1 Branch")
	h.until("the question", h.shows("Delete branch merged-one at "+rev("merged-one")[:10]+"?"))
	h.click("Delete")
	h.until("the delete", func() bool { return rev("refs/heads/merged-one") == "" })
	waitIdle("the delete ended")
	h.until("the restore button", h.shows("Deleted merged-one at "))
	gone := rev("HEAD~1")
	h.click("Restore")
	h.until("restored", func() bool { return rev("refs/heads/merged-one") == gone })
	waitIdle("the restore ended")

	// A tag, annotated, and a remote added.
	h.click("Tags")
	h.click("New Tag…")
	h.until("the tag dialog", h.shows("Lightweight"))
	tb.refs.dlg.values[0] = "v1"
	tb.refs.dlg.message = "release"
	h.click("Create")
	h.until("the tag", func() bool { return rev("refs/tags/v1") != "" })
	if typ := strings.TrimSpace(r.Git("cat-file", "-t", "v1")); typ != "tag" {
		t.Fatalf("v1 is a %s", typ)
	}
	waitIdle("the tag ended")
	h.click("Remotes")
	h.until("the remotes", h.shows(up.Dir))
	h.click("Add Remote…")
	h.until("the remote dialog", h.shows("Add Remote"))
	tb.refs.dlg.values = []string{"second", filepath.Join(up.Dir, ".git")}
	h.click("Add")
	h.until("the remote", func() bool { return strings.Contains(r.Git("remote"), "second") })
	h.until("the remote shown", h.shows("second"))
}

func hasExact(h *harness, s string) bool {
	for _, x := range h.tt.Texts() {
		if x == s {
			return true
		}
	}
	return false
}
