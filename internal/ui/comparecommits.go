package ui

import (
	"bytes"
	"encoding/hex"
	"errors"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/session"
	"ramus/locales"
)

// commitCompare is the comparison of two commits a tab shows, from the
// older to the newer as the user picked them.
type commitCompare struct {
	from, to         []byte
	fromName, toName string
	// branches compares two branches: by default (tips 0) what to changes
	// since it split off from, from their merge base, as git diff
	// from...to and a pull request do; tips 1 compares the two as they are.
	branches bool
	tips     int
	// base is the merge base once read, basing set while it is; note says
	// why the tips are compared instead, when there is none.
	base   []byte
	basing bool
	note   string
	// against is what d was compared from: from, or base.
	against []byte
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
	a.compare(t, commitCompare{from: from, to: to, fromName: fromName, toName: toName})
}

// compareBranches shows what the branch at to changes since it split off
// from the one at from, or how their tips differ, as the user picks.
func (a *App) compareBranches(t *tab, from, to []byte, fromName, toName string, tips int) {
	a.compare(t, commitCompare{from: from, to: to, fromName: fromName, toName: toName, branches: true, tips: tips})
}

func (a *App) compare(t *tab, cc commitCompare) {
	if t.cmp.cancel != nil {
		t.cmp.cancel()
	}
	cc.split = 360
	t.cmp = cc
	t.sel = "compare"
}

// compareFrom is the commit the comparison is read from: the merge base
// of two branches once read, nil while it is.
func (a *App) compareFrom(t *tab, cc *commitCompare) []byte {
	if !cc.branches || cc.tips == 1 || cc.note != "" {
		return cc.from
	}
	if cc.base == nil && !cc.basing {
		cc.basing = true
		from, to := cc.from, cc.to
		t.s.MergeBase(from, to, func(mb []byte, err error) {
			if !bytes.Equal(cc.from, from) || !bytes.Equal(cc.to, to) {
				return
			}
			cc.basing = false
			var none *session.NoMergeBaseError
			switch {
			case errors.As(err, &none) && none.Shallow:
				cc.note = locales.CompareShallowBase(redact.Control(cc.toName))
			case errors.As(err, &none):
				cc.note = locales.CompareNoBase(redact.Control(cc.fromName), redact.Control(cc.toName))
			case err != nil:
				cc.note = locales.CompareBaseFailed(errText(err))
			default:
				cc.base = mb
			}
		})
	}
	return cc.base
}

// shortID names a commit by the start of its hash.
func shortID(id []byte) string { return hex.EncodeToString(id)[:10] }

func (a *App) commitCompareView(c *ui.Context, t *tab) {
	th := c.Theme()
	cc := &t.cmp
	if cc.to == nil {
		return
	}
	if from := a.compareFrom(t, cc); cc.d == nil && !cc.loading && from != nil {
		cc.loading, cc.against = true, from
		to := cc.to
		cc.cancel = t.s.Compare(from, to, func(d session.CommitDetail) {
			if bytes.Equal(cc.against, from) && bytes.Equal(cc.to, to) {
				// Listed first, then again counted.
				cc.loading, cc.d = !d.Done, &d
			}
		})
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Text(c, redact.Control(cc.fromName)).Font("monospace").SingleLine()
			ui.Text(c, "→").TextColor(th.TextMuted)
			ui.Text(c, redact.Control(cc.toName)).Font("monospace").SingleLine().Grow(1)
			if cc.branches {
				was := cc.tips
				ui.Segmented(c, &cc.tips, locales.CompareSinceSplit(), locales.CompareTipToTip())
				if cc.tips != was {
					a.compareBranches(t, cc.from, cc.to, cc.fromName, cc.toName, cc.tips)
				}
			}
			if ui.Button(c, locales.CompareSwap()).Clicked() {
				if cc.branches {
					a.compareBranches(t, cc.to, cc.from, cc.toName, cc.fromName, cc.tips)
				} else {
					a.compareCommits(t, cc.to, cc.from, cc.toName, cc.fromName)
				}
			}
		})
		if cc.note != "" && cc.tips == 0 {
			ui.Text(c, cc.note).FontSize(textSmall).TextColor(th.TextMuted).Padding(0, insetX, spaceS, insetX).Selectable()
		}
		d := cc.d
		if d == nil {
			loading(c, "")
			return
		}
		if d.Err != nil {
			errorMessage(c, errText(d.Err))
			return
		}
		ui.Split(c, &cc.split, func() { a.commitFiles(c, t, &cc.cv, d, nil) }, func() { a.commitPatch(c, t, &cc.cv, d) }).SplitReset(360).Fill()
	})
}
