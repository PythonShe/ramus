package main

import (
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo"
)

// probeEnv, when set, makes the app a measure of its own cold start: it
// shows nowhere (no Dock icon, its window drawn hidden), keeps its data in
// a fresh folder it removes, and prints the time from the start of main
// to its first frame before it exits. scripts/coldstart times the same
// from outside, from exec on.
const probeEnv = "RAMUS_PROBE_STARTUP"

// probeTimeout ends a probe whose first frame never comes.
const probeTimeout = 30 * time.Second

var mainStart time.Time

// startProbe readies a probe run and returns its data folder, "" when
// this is not one.
func startProbe() string {
	if os.Getenv(probeEnv) == "" {
		return ""
	}
	dir, err := os.MkdirTemp("", "ramus-probe-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyProhibited)
	time.AfterFunc(probeTimeout, func() {
		fmt.Fprintln(os.Stderr, "probe: no first frame within", probeTimeout)
		os.RemoveAll(dir)
		os.Exit(1)
	})
	return dir
}

// probeWindow has win report its first frame, then ends the app.
func probeWindow(win *mygo.Window, dir string) {
	win.OnReadyToShow(func() {
		fmt.Printf("first frame %.1f ms\n", float64(time.Since(mainStart).Microseconds())/1000)
		os.RemoveAll(dir)
		mygo.App.Exit(0)
	})
}
