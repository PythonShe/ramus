package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Enter answers a question with its default: Cancel before an action
// that destroys, the action before one that loses nothing. Escape
// cancels either.
func TestConfirmDefault(t *testing.T) {
	for _, tc := range []struct {
		safe bool
		key  ui.Key
		ran  bool
	}{
		{false, ui.KeyEnter, false},
		{true, ui.KeyEnter, true},
		{true, ui.KeyEscape, false},
	} {
		h := newHarness(t)
		ran := false
		h.app.confirm = confirmState{open: true, title: "Delete it?", button: "Delete", safe: tc.safe, run: func() { ran = true }}
		h.tt.Frame()
		h.tt.Frame()
		if !h.tt.HasText("Delete it?") {
			t.Fatal("no question shown")
		}
		h.tt.Key(0, tc.key)
		h.tt.Frame()
		h.tt.Frame()
		if ran != tc.ran || h.app.confirm.open {
			t.Errorf("safe %v, key %v: ran %v, open %v", tc.safe, tc.key, ran, h.app.confirm.open)
		}
	}
}

// The action of a question runs when clicked, destroying or not.
func TestConfirmClick(t *testing.T) {
	h := newHarness(t)
	ran := false
	h.app.confirm = confirmState{open: true, title: "Delete it?", button: "Delete", run: func() { ran = true }}
	h.tt.Frame()
	h.click("Delete")
	h.tt.Frame()
	if !ran {
		t.Fatal("the action did not run")
	}
}
