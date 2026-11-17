package ui

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/egoist/mygo/ui"
)

// Every glyph is parsed, and mygo draws all it holds.
func TestGlyphs(t *testing.T) {
	set := glyphs()
	g := reflect.ValueOf(&set).Elem()
	for i := range g.NumField() {
		name := g.Type().Field(i).Name
		f := g.Field(i)
		// The fields are unexported, which reflect reads only so.
		svg := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface().(*ui.SVG)
		if svg == nil {
			t.Errorf("glyph %s is not set", name)
			continue
		}
		if u := svg.Unsupported(); len(u) > 0 {
			t.Errorf("glyph %s holds what mygo does not draw: %q", name, u)
		}
	}
}
