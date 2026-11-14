package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/tools"
)

// issueURL is where a user files a report, in their browser and in their
// own words: the app sends nothing.
const issueURL = "https://github.com/PythonShe/ramus/issues/new"

// diagState is the dialog showing the diagnostics the user may copy into
// a report, exactly as they would be copied.
type diagState struct {
	open bool
	// crash is the latest crash report, read off the main thread; crashName
	// its file, "" when there is none.
	crash, crashName string
	withCrash        bool
}

func (a *App) openDiagnostics() {
	a.diag = diagState{open: true}
	if a.Crash == nil {
		return
	}
	crash, update := a.Crash, a.Update
	go func() {
		names, _ := crash.Reports()
		if len(names) == 0 {
			return
		}
		data, err := os.ReadFile(names[0])
		if err != nil {
			return
		}
		update(func() { a.diag.crash, a.diag.crashName = string(data), filepath.Base(names[0]) })
	}()
}

// diagnostics is the text copied: the app, OS and git; what each open
// repository needs of git; no path, name or content of a repository.
func (a *App) diagnostics() string {
	var b strings.Builder
	if a.Crash != nil {
		git := ""
		if a.Runner != nil {
			git = a.Runner.Git().Version.Raw
		}
		b.WriteString(a.Crash.Environment(git))
	} else {
		fmt.Fprintf(&b, "OS: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	}
	if a.Runner != nil {
		fmt.Fprintf(&b, "Git processes running: %d\n", a.Runner.Running())
	}
	fmt.Fprintf(&b, "Open repositories: %d\n", len(a.tabs))
	for i, t := range a.tabs {
		fmt.Fprintf(&b, "  %d: %s\n", i+1, repoDiagnostics(t))
	}
	if a.Settings.FetchMinutes > 0 {
		fmt.Fprintf(&b, "Background fetch: every %d minutes\n", a.Settings.FetchMinutes)
	}
	if a.diag.withCrash && a.diag.crash != "" {
		b.WriteString("\n" + strings.TrimRight(a.diag.crash, "\n") + "\n")
	}
	return b.String()
}

func repoDiagnostics(t *tab) string {
	st := t.s.State
	r := t.s.Repo
	switch {
	case st.Opening:
		return "opening"
	case st.Err != nil, r == nil:
		return "could not open"
	}
	parts := []string{r.Caps.ObjectFormat, r.Caps.RefStorage + " refs"}
	if st.Restricted {
		parts = append(parts, "not trusted")
	}
	if f := st.Fallbacks; len(f) > 0 {
		parts = append(parts, "through git: "+strings.Join(f, ", "))
	}
	if st.Operation != "" {
		parts = append(parts, "in "+string(st.Operation))
	}
	if st.WatchErr != nil {
		parts = append(parts, "not watching")
	}
	if t.s.Panic != nil {
		parts = append(parts, "recovered a panic")
	}
	return strings.Join(parts, "; ")
}

func (a *App) diagnosticsView(c *ui.Context) {
	d := &a.diag
	if !d.open {
		return
	}
	text := a.diagnostics()
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(640).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Diagnostics").Bold()
			ui.Text(c, "Copy puts exactly this on the clipboard. Nothing is sent.").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Scroll(c).MaxHeight(380).FillWidth().Background(c.Theme().Surface).Children(func() {
				ui.Text(c, text).Font("monospace").FontSize(12).Selectable().Padding(8)
			})
			if d.crashName != "" {
				ui.Checkbox(c, &d.withCrash, "Include the latest crash report ("+d.crashName+")")
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "Open Issue Page").Clicked() {
					go tools.OpenDefault(issueURL)
				}
				ui.Spacer(c)
				if ui.Button(c, "Close").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Copy Diagnostics").Clicked() {
					a.clip = text
				}
			})
		})
	})
}
