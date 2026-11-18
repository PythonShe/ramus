package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

type fakeUpdate struct {
	version   string
	installed *bool
}

func (u fakeUpdate) Version() string { return u.version }

func (u fakeUpdate) Install(ctx context.Context, progress func(done, total int64)) error {
	progress(1, 2)
	*u.installed = true
	return nil
}

// The About window shows the version, checks for a newer one as asked,
// installs it and relaunches, and, behind Licenses, lists every license,
// each shown whole; Back goes from a license to the list, and from the
// list to the version.
func TestAboutWindow(t *testing.T) {
	ab := NewAbout("1.2.3")
	posted := make(chan func(), 8)
	ab.Update = func(fn func()) { posted <- fn }
	var found Update
	var err error
	ab.Check = func(context.Context) (Update, error) { return found, err }
	relaunched, installed := false, false
	ab.Relaunch = func() { relaunched = true }
	tt := ui.NewTester(ab.View, 440, 620)
	tt.Frame()
	shows := func(s string) bool {
		for _, x := range tt.Texts() {
			if strings.Contains(x, s) {
				return true
			}
		}
		return false
	}
	// drain runs what the work off the main thread posted, until fn.
	drain := func(n int) {
		for range n {
			(<-posted)()
		}
		tt.Frame()
	}
	if !shows("Version 1.2.3") || !shows("Licenses") || shows("github.com/egoist/mygo") {
		t.Fatalf("texts %q", tt.Texts())
	}

	for _, c := range []struct {
		found Update
		err   error
		says  string
	}{
		{nil, nil, "Ramus is up to date."},
		{nil, ErrUpdatesOff, "does not update itself"},
		{nil, errors.New("offline"), "Could not check for updates: offline"},
	} {
		found, err = c.found, c.err
		if e := tt.Click("Check for Updates"); e != nil {
			t.Fatal(e)
		}
		tt.Frame()
		drain(1)
		if !shows(c.says) {
			t.Fatalf("checked with %v, texts %q", c.err, tt.Texts())
		}
	}

	found, err = fakeUpdate{"2.0.0", &installed}, nil
	if e := tt.Click("Check for Updates"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	drain(1)
	if !shows("Ramus 2.0.0 is available.") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if e := tt.Click("Install and Relaunch"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	drain(2) // the progress, then the end
	if !installed || !relaunched {
		t.Fatalf("installed %v, relaunched %v", installed, relaunched)
	}

	if e := tt.Click("Licenses"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if shows("Version 1.2.3") || !shows("golang.org/x/text") {
		t.Fatalf("the licenses not listed alone: %q", tt.Texts())
	}
	if e := tt.Click("github.com/egoist/mygo"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if !shows("Permission is hereby granted") || shows("golang.org/x/text") {
		t.Fatalf("mygo's license not shown alone: %q", tt.Texts())
	}
	if e := tt.Click("Back"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if !shows("golang.org/x/text") {
		t.Fatalf("not back at the list: %q", tt.Texts())
	}
	if e := tt.Click("Back"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if !shows("Version 1.2.3") || shows("golang.org/x/text") {
		t.Fatalf("not back at the version: %q", tt.Texts())
	}
}

func TestReflow(t *testing.T) {
	in := "MIT License\n\nCopyright (c) 2026 A\nCopyright (c) 2025 B\n\nPermission is hereby granted, free\n   of charge, to any person:\n\n1. Redistributions of source code must\n   retain the above.\n2. Second item.\n"
	want := "MIT License\n\nCopyright (c) 2026 A\nCopyright (c) 2025 B\n\nPermission is hereby granted, free of charge, to any person:\n\n1. Redistributions of source code must retain the above.\n2. Second item."
	if got := reflow(in); got != want {
		t.Errorf("reflow =\n%q\nwant\n%q", got, want)
	}
}

// The gear at the end of the bar, there before a repository is open
// too, opens About from its menu, which has one way there: the check
// for updates is in that window.
func TestAppButtonOpensAbout(t *testing.T) {
	h := newHarness(t)
	asked := 0
	h.app.ShowAbout = func() { asked++ }
	h.tt.Frame()
	h.tt.ClickAt(1200-insetX-14, spaceXS+14)
	h.tt.Frame()
	if items := h.tt.Menu(); slices.ContainsFunc(items, func(s string) bool { return strings.Contains(s, "Update") }) {
		t.Fatalf("menu %q", items)
	}
	if err := h.tt.ChooseMenuItem("About Ramus"); err != nil {
		t.Fatalf("%v; texts %q", err, h.tt.Texts())
	}
	h.tt.Frame()
	if asked != 1 {
		t.Fatalf("About opened %d times", asked)
	}
}
