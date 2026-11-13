package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gittest"
	"ramus/spike/internal/measure"
)

// TestScrollKernel builds the history view headless, on the CPU renderer,
// and times frames while it scrolls three rows a frame through the kernel:
//
//	RAMUS_FIXTURES=~/Projects/ramus-fixtures go test ./spike/history -run Kernel -v
func TestScrollKernel(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "linux", ".git")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	m := newModel(dir, 0, 0)
	updates := make(chan func(), 1024)
	m.start(func(fn func()) { updates <- fn })
	drain := func() {
		for {
			select {
			case fn := <-updates:
				fn()
			default:
				return
			}
		}
	}
	tt := ui.NewTester(m.view, 1200, 900)
	deadline := time.Now().Add(2 * time.Minute)
	for !m.done && m.err == nil && time.Now().Before(deadline) {
		(<-updates)()
	}
	if m.err != nil || !m.done {
		t.Fatalf("load: done=%v err=%v", m.done, m.err)
	}
	t.Logf("%s", m.status())
	if out := os.Getenv("RAMUS_SPIKE_PNG"); out != "" {
		// Let the text of the first rows arrive, then save what shows.
		for i := 0; i < 50 && len(m.text) == 0; i++ {
			tt.Frame()
			time.Sleep(20 * time.Millisecond)
			drain()
		}
		m.list.ScrollTo(52000, ui.Start)
		for i := 0; i < 50; i++ {
			tt.Frame()
			time.Sleep(10 * time.Millisecond)
			drain()
		}
		tt.Frame()
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
	}
	for _, step := range []int{3, 40} {
		var frames []time.Duration
		for i := 0; i < 300; i++ {
			drain()
			m.target += step
			m.list.ScrollTo(m.target, ui.Start)
			start := time.Now()
			tt.Frame()
			frames = append(frames, time.Since(start))
		}
		s := measure.Summarize(frames)
		t.Logf("headless frame, %d rows a frame: n=%d p50=%v p95=%v max=%v", step, s.N, s.P50, s.P95, s.Max)
	}
	// A jump far into old history, as "go to commit" does.
	m.target = m.snap.Len() - 100
	m.list.ScrollTo(m.target, ui.Start)
	start := time.Now()
	tt.Frame()
	t.Logf("jump to row %d: %v", m.target, time.Since(start))
	if !tt.HasText("…") && len(tt.Texts()) == 0 {
		t.Error("nothing drawn")
	}
}
