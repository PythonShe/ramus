package ui

import (
	"errors"
	"strings"
	"testing"
)

// Revealing a folder or opening the settings file with nothing to open
// it, as on a Linux without xdg-open, says so rather than do nothing.
func TestOpenFailureIsSaid(t *testing.T) {
	h := newHarness(t)
	h.app.goOpen(nil, func(string) error { return errors.New("tools: no file manager to open with") }, "/settings.json")
	h.until("the failure said", func() bool { return strings.Contains(h.app.notice, "no file manager") })
}
