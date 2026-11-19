package ui

import (
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/settings"
	"ramus/locales"
)

// A git older than Ramus runs is told as such, with the version found and
// the one needed, not as a git missing.
func TestGitTooOldIsTold(t *testing.T) {
	a := NewApp(Deps{Settings: settings.Defaults, Update: func(fn func()) { fn() }})
	a.SetRunner(nil, &gitexe.TooOldError{Path: "/usr/bin/git", Version: gitexe.Version{Major: 2, Minor: 30, Patch: 1}})
	if want := locales.AppGitTooOld("2.30.1", gitexe.Minimum.String()); a.notice != want {
		t.Fatalf("notice %q, want %q", a.notice, want)
	}
}
