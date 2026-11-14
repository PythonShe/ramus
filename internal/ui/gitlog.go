package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/run"
)

// gitLogState is the command log's view of every git command the app ran
// in the repository, reads included, as the runner's journal keeps them.
type gitLogState struct {
	// mode is which log shows: 0 actions and writes, 1 every git command.
	mode    int
	recs    []run.Record // oldest first
	version uint64
	loading bool
	list    ui.ListState
	chosen  int // newest first; -1 none
	out     ui.ListState
	// lines are the output of the record shown, split once.
	lines []string
	shown run.Record
}

// loadGitLog reads the journal again when it has moved on.
func (a *App) loadGitLog(t *tab) {
	gl := &t.gitLog
	v := t.s.CommandsVersion()
	if gl.loading || v == gl.version && gl.recs != nil {
		return
	}
	gl.loading = true
	t.s.GitCommands(func(recs []run.Record) {
		gl.loading, gl.version = false, v
		// Keep the same record chosen as newer ones come in above it.
		var was *run.Record
		if k := len(gl.recs) - 1 - gl.chosen; gl.chosen >= 0 && k >= 0 && k < len(gl.recs) {
			was = &gl.recs[k]
		}
		gl.chosen = -1
		for k := range recs {
			if was != nil && sameRecord(&recs[k], was) {
				gl.chosen = len(recs) - 1 - k
			}
		}
		gl.recs = recs
	})
}

func (a *App) gitLogView(c *ui.Context, t *tab) {
	th := c.Theme()
	gl := &t.gitLog
	a.loadGitLog(t)
	recs := gl.recs
	n := len(recs)
	gl.list.Selected = &gl.chosen
	gl.list.Label = func(i int) string { r := &recs[n-1-i]; return r.Short + ", " + recordStatus(r) }
	ui.Split(c, &t.listW, func() {
		ui.List(c, &gl.list, n, func(i int) {
			r := &recs[n-1-i]
			ui.Column(c).Padding(6, 8).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, r.Short).Font("monospace").FontSize(12).SingleLine().Grow(1)
					ui.Text(c, recordStatus(r)).FontSize(12).TextColor(recordColor(th, r))
				})
				ui.Text(c, r.Started.Format(time.TimeOnly)).FontSize(12).TextColor(th.TextMuted)
			})
		}).Label("Git commands run").Fill()
	}, func() {
		k := n - 1 - gl.chosen
		if gl.chosen < 0 || k < 0 || k >= n {
			return
		}
		r := &recs[k]
		if !sameRecord(r, &gl.shown) {
			gl.shown = *r
			gl.lines = strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
			if r.Stdout == "" {
				gl.lines = nil
			}
		}
		ui.Column(c).Fill().Padding(12).Gap(8).Children(func() {
			ui.Text(c, r.Line).Font("monospace").FontSize(12).TextColor(th.TextMuted).Selectable()
			if r.Err != "" {
				ui.Text(c, r.Err).TextColor(recordColor(th, r)).Selectable()
			}
			if r.Stderr != "" {
				ui.Text(c, strings.TrimSpace(r.Stderr)).Font("monospace").FontSize(12).Selectable()
			}
			ui.List(c, &gl.out, len(gl.lines), func(i int) {
				ui.Text(c, gl.lines[i]).Font("monospace").FontSize(12).Selectable()
			}).Label("Command output").Grow(1)
			if more := r.StdoutLen - len(r.Stdout); r.StdoutLen > 0 && more > 0 {
				ui.Text(c, fmt.Sprintf("%d more bytes not kept", more)).FontSize(12).TextColor(th.TextMuted)
			}
		})
	}).Fill()
}

// recordStatus is how a command ended, for the log.
func recordStatus(r *run.Record) string {
	switch {
	case r.Long:
		return "started"
	case r.Stopped:
		return "stopped"
	case r.Err != "" && r.Exit > 0:
		return "exit " + strconv.Itoa(r.Exit)
	case r.Err != "":
		return "failed"
	}
	return r.Duration.Round(time.Millisecond).String()
}

func recordColor(th *ui.Theme, r *run.Record) ui.Color {
	// Exit 1 is how config --get, merge-base --is-ancestor and diff
	// --quiet answer no, which the engine asks all the time.
	if r.Err != "" && !r.Stopped && r.Exit != 1 {
		return th.Danger
	}
	return th.TextMuted
}

func sameRecord(a, b *run.Record) bool { return a.Started.Equal(b.Started) && a.Line == b.Line }
