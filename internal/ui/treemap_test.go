package ui

import (
	"fmt"
	"testing"
)

// The paper's example: areas 6, 6, 4, 3, 2, 2, 1 in a 6 by 4 rectangle.
func TestSquarify(t *testing.T) {
	var tiles []tile
	for i, w := range []float64{6, 6, 4, 3, 2, 2, 1} {
		tiles = append(tiles, tile{node: 0, w: w, other: i})
	}
	strips := squarify(tiles, 6, 4)
	got := ""
	n := 0
	for _, s := range strips {
		got += fmt.Sprint(s.across, len(s.tiles), s.w, " ")
		n += len(s.tiles)
	}
	if want := "false 2 12 true 2 7 false 1 2 false 1 2 true 1 1 "; got != want || n != 7 {
		t.Errorf("strips %q, want %q", got, want)
	}
	if squarify(nil, 1, 1) != nil || squarify(tiles, 0, 1) != nil {
		t.Error("empty space laid out")
	}
}
