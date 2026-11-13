package main

import (
	"log"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"ramus/internal/ui"
)

func main() {
	app := &ui.App{}
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:     "Ramus",
			Width:     1200,
			Height:    800,
			MinWidth:  720,
			MinHeight: 480,
			StateKey:  "main",
			Content:   mygoui.View(app.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
