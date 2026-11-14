package ui

import (
	"fmt"
	"testing"

	"ramus/internal/session"
	"ramus/internal/ui/pathtree"
)

// wideTreemap is a treemap of one folder holding n files.
func wideTreemap(n int) *treemapData {
	var pw session.PathWeights
	for i := range n {
		pw.Paths = append(pw.Paths, fmt.Sprintf("d/f%06d", i)...)
		pw.Paths = append(pw.Paths, 0)
		pw.Weights = append(pw.Weights, int64(i%97+1))
	}
	return newTreemapData(pw)
}

// The folder shown keeps its tiles and their places from frame to frame,
// placed again only when the space changes.
func TestTreemapKeepsTheFolderShown(t *testing.T) {
	d := wideTreemap(1000)
	dir := d.tree.Children(pathtree.Root)[0]
	tiles := d.tilesOf(dir)
	if len(tiles) != maxTiles || tiles[len(tiles)-1].other != 1000-maxTiles+1 {
		t.Fatalf("%d tiles", len(tiles))
	}
	laid := d.layout(dir, 400, 300)
	if again := d.layout(dir, 400, 300); &again[0] != &laid[0] || &d.tilesOf(dir)[0] != &tiles[0] {
		t.Fatal("the same folder and space made its tiles again")
	}
	if wider := d.layout(dir, 800, 300); &wider[0] == &laid[0] {
		t.Fatal("a new space kept the old places")
	}
	if top := d.tilesOf(pathtree.Root); len(top) != 1 || top[0].node != dir {
		t.Fatalf("the top folder: %+v", top)
	}
	if again := d.tilesOf(dir); &again[0] == &tiles[0] {
		t.Fatal("tiles kept for a folder no longer shown")
	}
}

func BenchmarkTreemapFrame(b *testing.B) {
	d := wideTreemap(50000)
	dir := d.tree.Children(pathtree.Root)[0]
	b.Run("made", func(b *testing.B) {
		for range b.N {
			place(squarify(d.makeTiles(dir), 800, 600), 800, 600)
		}
	})
	b.Run("kept", func(b *testing.B) {
		for range b.N {
			d.layout(dir, 800, 600)
		}
	})
}
