package ui

import (
	"context"
	"errors"
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
// installs it and relaunches, and lists every license, each shown whole.
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
	if !shows("Version 1.2.3") || !shows("Licenses") || !shows("github.com/egoist/mygo") {
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

	if e := tt.Click("github.com/egoist/mygo"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if !shows("Permission is hereby granted") || shows("golang.org/x/text") {
		t.Fatalf("mygo's license not shown alone: %q", tt.Texts())
	}
	if e := tt.Click("All Licenses"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if !shows("golang.org/x/text") {
		t.Fatalf("not back at the list: %q", tt.Texts())
	}
}

func TestReflow(t *testing.T) {
	in := "MIT License\n\nCopyright (c) 2026 A\nCopyright (c) 2025 B\n\nPermission is hereby granted, free\n   of charge, to any person:\n\n1. Redistributions of source code must\n   retain the above.\n2. Second item.\n"
	want := "MIT License\n\nCopyright (c) 2026 A\nCopyright (c) 2025 B\n\nPermission is hereby granted, free of charge, to any person:\n\n1. Redistributions of source code must retain the above.\n2. Second item."
	if got := reflow(in); got != want {
		t.Errorf("reflow =\n%q\nwant\n%q", got, want)
	}
}
