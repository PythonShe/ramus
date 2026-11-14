package ui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
)

// monitorOffer offers git's file system monitor and the untracked cache
// when status was slow, with the exact commands and what keeps running
// afterwards: the daemon outlives the app, which a user would not expect.
func (a *App) monitorOffer(c *ui.Context, t *tab) {
	m := t.s.State.Monitor
	if m == nil || t.s.Queue == nil {
		return
	}
	th := c.Theme()
	var lines []string
	for _, cmd := range m.Cmds {
		lines = append(lines, "git "+strings.Join(cmd.Args, " "))
	}
	ui.Column(c).Padding(8).Gap(6).Background(th.Surface).FillWidth().Children(func() {
		ui.Text(c, m.Reason).FontSize(12)
		ui.Text(c, redact.Control(strings.Join(lines, "\n"))).Font("monospace").FontSize(12).Selectable()
		if m.Daemon != "" {
			ui.Text(c, m.Daemon).FontSize(12).TextColor(th.TextMuted).Selectable()
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Turn On").Clicked() {
				if _, err := t.s.TurnOnMonitor(*m); err != nil {
					a.notice = errText(err)
				}
			}
			if ui.Button(c, "Not Now").Clicked() {
				t.s.DismissMonitor()
			}
		})
	})
}
