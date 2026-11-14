package ui

import (
	"bytes"
	"encoding/hex"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/session"
)

// commitCompare is the comparison of two commits a tab shows, from the
// older to the newer as the user picked them.
type commitCompare struct {
	from, to         []byte
	fromName, toName string
	// d is the comparison once read; cv holds the file chosen and its
	// patch.
	d       *session.CommitDetail
	loading bool
	cancel  func()
	cv      commitView
	split   float32
}

// compareCommits shows what changed from commit from to commit to.
func (a *App) compareCommits(t *tab, from, to []byte, fromName, toName string) {
	if t.cmp.cancel != nil {
		t.cmp.cancel()
	}
	t.cmp = commitCompare{from: from, to: to, fromName: fromName, toName: toName, split: 360}
	t.sel = "compare"
}

// shortID names a commit by the start of its hash.
func shortID(id []byte) string { return hex.EncodeToString(id)[:10] }

func (a *App) commitCompareView(c *ui.Context, t *tab) {
	th := c.Theme()
	cc := &t.cmp
	if cc.to == nil {
		return
	}
	if cc.d == nil && !cc.loading {
		cc.loading = true
		from, to := cc.from, cc.to
		cc.cancel = t.s.Compare(from, to, func(d session.CommitDetail) {
			if bytes.Equal(cc.from, from) && bytes.Equal(cc.to, to) {
				cc.loading, cc.d = false, &d
			}
		})
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(8, 12).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Text(c, redact.Control(cc.fromName)).Font("monospace").SingleLine()
			ui.Text(c, "→").TextColor(th.TextMuted)
			ui.Text(c, redact.Control(cc.toName)).Font("monospace").SingleLine().Grow(1)
			if ui.Button(c, "Swap").Clicked() {
				a.compareCommits(t, cc.to, cc.from, cc.toName, cc.fromName)
			}
		})
		d := cc.d
		if d == nil {
			ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			return
		}
		if d.Err != nil {
			ui.Text(c, errText(d.Err)).TextColor(th.Danger).Padding(12).Selectable()
			return
		}
		ui.Split(c, &cc.split, func() { a.commitFiles(c, &cc.cv, d) }, func() { a.commitPatch(c, t, &cc.cv, d) }).SplitReset(360).Fill()
	})
}
