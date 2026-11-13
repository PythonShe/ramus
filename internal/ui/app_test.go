package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestView(t *testing.T) {
	a := &App{}
	tt := ui.NewTester(a.View, 1200, 800)
	if !tt.HasText("Ramus") {
		t.Errorf("texts %q", tt.Texts())
	}
}
