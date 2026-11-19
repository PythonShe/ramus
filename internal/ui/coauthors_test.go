package ui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Co-authors are chosen among the repository's authors, but the user, or
// typed in whole; one taken off is not written; those left go in the
// commit's trailers. The user is who git commits as, here named by the
// environment and in no config.
func TestCoAuthorPicker(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for _, who := range []string{"Ada Lovelace <ada@example.com>", "Alan Turing <alan@example.com>"} {
		name, email, _ := strings.Cut(strings.TrimSuffix(who, ">"), " <")
		r.Env = []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email}
		r.Write("x", who)
		r.Commit("by " + name)
	}
	r.Env = nil
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	r.Write("a.txt", "a")
	r.Git("add", "a.txt")
	h.until("staged", h.shows("Staged (1)"))
	cs := &h.app.current().commit

	h.click("Add Co-Author")
	h.until("the people", h.shows("alan@example.com"))
	if !h.tt.HasText("ada@example.com") || h.tt.HasText("Ramus Test") {
		t.Fatalf("people %q", h.tt.Texts())
	}
	h.tt.Type("alan")
	h.tt.Frame()
	if h.tt.HasText("ada@example.com") {
		t.Fatal("Ada matches alan")
	}
	h.tt.Key(0, ui.KeyEnter)
	h.until("Alan chosen", func() bool { return cs.coAuthors == "Alan Turing <alan@example.com>" })

	h.click("Add Co-Author")
	h.until("the people", h.shows("ada@example.com"))
	if h.tt.HasText("alan@example.com") {
		t.Fatal("Alan offered again")
	}
	h.tt.Type("Grace Hopper <grace@example.com>")
	h.until("the row adding her", h.shows("Add Grace Hopper <grace@example.com>"))
	h.tt.Key(0, ui.KeyEnter)
	h.until("Grace chosen", func() bool { return strings.HasSuffix(cs.coAuthors, ", Grace Hopper <grace@example.com>") })
	h.click("Remove Alan Turing")
	if cs.coAuthors != "Grace Hopper <grace@example.com>" {
		t.Fatalf("co-authors %q", cs.coAuthors)
	}

	cs.subject = "Together"
	h.until("ready", func() bool { return commitReady(h.app.current()) })
	h.click("Commit to main")
	h.until("the commit", func() bool { return strings.Contains(r.Git("log", "-1", "--format=%B"), "Together") })
	if msg := r.Git("log", "-1", "--format=%B"); !strings.Contains(msg, "Co-authored-by: Grace Hopper <grace@example.com>") || strings.Contains(msg, "Alan") {
		t.Fatalf("message %q", msg)
	}
	h.until("the chips gone", func() bool { return cs.coAuthors == "" && !h.tt.HasText("Grace Hopper") })
}

// A name may hold a comma or a semicolon, as "Doe, John": the person is
// kept whole, among others and as a draft keeps them.
func TestCoAuthorNameWithComma(t *testing.T) {
	stored := strings.Join(append(coAuthorList("Ann <ann@example.com>"), coAuthorRow{name: "Doe, John", email: "john@example.com"}.person(),
		coAuthorRow{name: "Roe; Jane", email: "jane@example.com"}.person()), ", ")
	got := coAuthorList(stored)
	want := []string{"Ann <ann@example.com>", "Doe, John <john@example.com>", "Roe; Jane <jane@example.com>"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("stored %q read back as %q", stored, got)
	}
	if m := typedPerson.FindStringSubmatch(got[1]); m == nil || strings.TrimSpace(m[1]) != "Doe, John" {
		t.Fatalf("the chip of %q names %q", got[1], m)
	}
}
