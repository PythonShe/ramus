// Command history shows a repository's whole history in a ui.List with a
// drawn graph column, and measures frame time while it scrolls.
//
//	go run ./spike/history -repo ~/Projects/ramus-fixtures/linux -autoscroll 3
//
// With -autoscroll the list scrolls that many rows a frame, from the top,
// for -frames frames, then prints the frame intervals and quits. Run with
// MYGO_FRAME_STATS=all to have mygo log each frame's build, layout, paint
// and present times as well.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"
)

func main() {
	repo := flag.String("repo", ".", "repository work tree or git directory")
	auto := flag.Int("autoscroll", 0, "rows to scroll a frame; 0 to scroll by hand")
	frames := flag.Int("frames", 600, "frames to measure with -autoscroll")
	flag.Parse()
	gitDir := *repo
	if _, err := os.Stat(filepath.Join(gitDir, ".git")); err == nil {
		gitDir = filepath.Join(gitDir, ".git")
	}
	m := newModel(gitDir, *auto, *frames)
	m.quit = mygo.App.Quit
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:   "Ramus spike: history",
			Width:   1200,
			Height:  900,
			Content: mygoui.View(m.view),
		})
		m.start(win.Update)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
