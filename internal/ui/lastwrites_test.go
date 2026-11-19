package ui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"ramus/internal/library"
)

// A write that runs after a later one of the same thing was asked is
// dropped, whatever order the goroutines run in; one of another thing is
// not.
func TestLastWritesDropTheOlder(t *testing.T) {
	var lw lastWrites
	first := lw.ask("a")
	other := lw.ask("b")
	second := lw.ask("a")
	var wrote []string
	if !lw.do("a", second, func() { wrote = append(wrote, "second") }) {
		t.Fatal("the last write asked did not run")
	}
	if lw.do("a", first, func() { wrote = append(wrote, "first") }) {
		t.Fatal("the older write ran after the later one")
	}
	if !lw.do("b", other, func() { wrote = append(wrote, "other") }) {
		t.Fatal("the write of another key was dropped")
	}
	if !slices.Equal(wrote, []string{"second", "other"}) {
		t.Fatalf("wrote %v", wrote)
	}
	if len(lw.asked) != 0 {
		t.Fatalf("still asked: %v", lw.asked)
	}
}

// A write held while a later one of the same thing is asked and runs
// after it: both land, in the order asked.
func TestLastWritesInTurn(t *testing.T) {
	var lw lastWrites
	first := lw.ask("a")
	held, release := make(chan struct{}), make(chan struct{})
	var wrote []string
	done := make(chan bool)
	go func() {
		done <- lw.do("a", first, func() {
			close(held)
			<-release
			wrote = append(wrote, "first")
		})
	}()
	<-held
	second := lw.ask("a")
	go func() { done <- lw.do("a", second, func() { wrote = append(wrote, "second") }) }()
	close(release)
	if !<-done || !<-done {
		t.Fatal("a write was dropped")
	}
	if !slices.Equal(wrote, []string{"first", "second"}) {
		t.Fatalf("wrote %v", wrote)
	}
}

// Pins and hidden refs toggled in quick succession leave the lists of
// the last toggle in the library's file.
func TestRefPrefsLastToggleKept(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(h.closeAll)
	r := h.repoWith()
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	for i := range 40 {
		h.app.toggleRef(tb, &tb.prefs.pinned, fmt.Sprintf("refs/heads/b%d", i%3))
		h.app.toggleRef(tb, &tb.prefs.hidden, fmt.Sprintf("refs/heads/h%d", i%5))
	}
	if !h.app.WaitWrites(10 * time.Second) {
		t.Fatal("the library was not written in time")
	}
	back, err := library.Load(h.lib.Path())
	if err != nil {
		t.Fatal(err)
	}
	pinned, hidden := back.RefPrefs(r.Dir)
	if !slices.Equal(pinned, tb.prefs.pinned) || !slices.Equal(hidden, tb.prefs.hidden) {
		t.Fatalf("library file has pinned %q, hidden %q; the tab %q, %q", pinned, hidden, tb.prefs.pinned, tb.prefs.hidden)
	}
}
