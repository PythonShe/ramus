package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/locales"
)

// confirmView shows a pending question as an alert: Cancel, then the
// action. An action that loses nothing is the default; one that destroys
// is in the Danger color with Cancel the default, which ui.AlertDialog
// cannot show, as it makes its last button the default in the accent
// color. Escape and a click outside answer Cancel.
func (a *App) confirmView(c *ui.Context, q *confirmState) {
	if !q.open {
		return
	}
	th := c.Theme()
	chosen := false
	a.modal(c, &q.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			ui.Text(c, q.title).Bold().FontSize(th.FontSize + 1)
			if q.message != "" {
				ui.Text(c, q.message).TextColor(th.TextMuted).Selectable()
			}
			ui.Row(c).Wrap().Gap(spaceM).Justify(ui.End).Margin(spaceM, 0, 0, 0).Children(func() {
				cancel := ui.Button(c, locales.CommonCancel())
				var act ui.Element
				if q.safe {
					act = ui.PrimaryButton(c, q.button).AutoFocus()
				} else {
					cancel.AutoFocus()
					act = dangerButton(c, q.button, false)
				}
				if cancel.Clicked() {
					q.open = false
				}
				if act.Clicked() {
					q.open, chosen = false, true
				}
			})
		})
	}).Role(ui.RoleAlertDialog).Label(q.title).Description(q.message)
	if chosen && q.run != nil {
		run := q.run
		q.run = nil
		run()
	}
}
