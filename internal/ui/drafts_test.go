package ui

import (
	"testing"

	"ramus/internal/drafts"
	"ramus/internal/library"
)

// A message left unfinished comes back when the repository opens again,
// before the commit template; the template alone is no draft, and a
// commit clears it.
func TestCommitDraft(t *testing.T) {
	h := newHarness(t)
	store := drafts.New(t.TempDir())
	h.app.Drafts = store
	r := h.repoWith()
	r.Write(".tpl", "Subject line\n\n# Explain why\n")
	r.Git("config", "commit.template", ".tpl")
	h.trust.Set(r.GitDir(), true)
	path := library.Clean(r.Dir)
	stored := func() drafts.Draft {
		d, err := store.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	open := func() *commitState {
		h.app.Open(r.Dir)
		h.until("the commit box", h.shows("Commit to main"))
		cs := &h.app.current().commit
		h.until("the draft read", func() bool { return cs.draftRead && cs.setup != nil })
		return cs
	}

	cs := open()
	if cs.subject != "Subject line" || !stored().Empty() {
		t.Fatalf("template %q, stored %+v", cs.subject, stored())
	}
	cs.subject, cs.body, cs.coAuthors = "Half done", "More to say", "A <a@example.invalid>"
	h.tt.Frame()
	want := drafts.Draft{Subject: "Half done", Body: "More to say", CoAuthors: "A <a@example.invalid>"}
	h.until("the draft kept", func() bool { return stored() == want })

	h.closeAll()
	h.app.Close(0)
	cs = open()
	if got := (drafts.Draft{Subject: cs.subject, Body: cs.body, CoAuthors: cs.coAuthors}); got != want {
		t.Fatalf("reopened with %+v", got)
	}

	// Committed: the draft goes, the template is back.
	r.Write("a.txt", "a")
	r.Git("add", "a.txt")
	h.until("staged", h.shows("Staged (1)"))
	h.click("Commit to main")
	h.until("the commit", func() bool { return !cs.busy && cs.subject == "Subject line" })
	h.until("the draft gone", func() bool { return stored().Empty() })
}
