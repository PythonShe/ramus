// Command diff shows a 50 MB text diff in editor.DiffView and measures the
// time to its first paint, the frames while it scrolls, and peak memory.
//
//	go run ./spike/diff -mode text|patch|patchfiles [-size 50]
//
// text has the view diff the two texts itself; patch shows git's hunks
// alone (SetPatch); patchfiles shows the whole files aligned by git's
// patch (SetPatchFiles), as Ramus would for a file that changed.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"ramus/spike/internal/measure"

	"ramus/spike/internal/quietwin"
)

func main() {
	mode := flag.String("mode", "text", "text, patch or patchfiles")
	size := flag.Int("size", 50, "megabytes per side")
	flag.Parse()
	t0 := time.Now()
	old, new := generate(*size << 20)
	gen := time.Since(t0)
	s, err := newSpike(*mode, old, new)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("mode %s, %d MB a side: generated in %v, git diff %v, buffers %v\n",
		*mode, *size, gen.Round(time.Millisecond), s.gitTime.Round(time.Millisecond), s.setupTime.Round(time.Millisecond))
	s.quit = mygo.App.Quit
	quietwin.Prepare()
	mygo.App.WhenReady(func() {
		quietwin.Open(mygo.WindowOptions{Title: "Ramus spike: diff", Width: 1400, Height: 900, Content: ui.View(s.view)})
		s.shown = time.Now()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

type spike struct {
	diff      *editor.DiffState
	gitTime   time.Duration
	setupTime time.Duration
	shown     time.Time
	painted   bool
	frames    []time.Duration
	last      time.Time
	quit      func()
}

func newSpike(mode, old, new string) (*spike, error) {
	s := &spike{}
	d := &editor.DiffState{Layout: editor.DiffSplit}
	switch mode {
	case "text":
		t := time.Now()
		d.Old, d.New = editor.NewBuffer(old), editor.NewBuffer(new)
		s.setupTime = time.Since(t)
	case "patch", "patchfiles":
		t := time.Now()
		patch, err := gitDiff(old, new)
		if err != nil {
			return nil, err
		}
		s.gitTime = time.Since(t)
		t = time.Now()
		files, err := editor.ParsePatch(patch)
		if err != nil || len(files) != 1 {
			return nil, fmt.Errorf("parse patch: %v (%d files)", err, len(files))
		}
		if mode == "patch" {
			d.SetPatch(files[0])
		} else if err := d.SetPatchFiles(files[0], old, new); err != nil {
			return nil, err
		}
		s.setupTime = time.Since(t)
	default:
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	s.diff = d
	return s, nil
}

func (s *spike) view(c *ui.Context) {
	ui.Column(c).Fill().Children(func() {
		editor.DiffView(c, s.diff).Grow(1).FillWidth()
		ui.Box(c).Height(1).Draw(func(p *ui.Painter, r ui.Rect) {
			if !s.painted {
				s.painted = true
				fmt.Printf("first paint %v after the window opened\n", time.Since(s.shown).Round(time.Millisecond))
			}
		})
	})
	if !s.painted {
		return
	}
	// Scroll a screen's worth of lines a frame, then report.
	now := time.Now()
	if !s.last.IsZero() {
		s.frames = append(s.frames, now.Sub(s.last))
	}
	s.last = now
	if len(s.frames) < 300 {
		s.diff.SetScrollY(s.diff.ScrollY() + 400)
		c.AnimationFrame()
		return
	}
	if s.quit != nil {
		measure.Summarize(s.frames).Print(os.Stdout, "scroll frame interval")
		fmt.Printf("peak RSS %d MB\n", peakRSS()>>20)
		s.quit()
		s.quit = nil
	}
}
