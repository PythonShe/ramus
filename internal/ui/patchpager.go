package ui

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"ramus/internal/session"
)

// Patch reads step up: a first page that shows at once, then the rest up
// to shownPatch without asking, then on request the whole patch, its
// files unfolded up to the same size.
// Variables so tests can step through them with small files.
var (
	firstPatchPage = 1 << 20
	// shownPatch bounds the patch read before the user asks for all.
	shownPatch = 32 << 20
	// wholePatch bounds what "Load Whole Patch" reads: far past any patch
	// a person reads, as git's own output is bounded.
	wholePatch = 512 << 20
)

// patchPager is where a patch's reading is: the first page, the rest, or
// the whole.
type patchPager struct{ stage int }

const (
	pageFirst = iota
	pageRest
	pageWhole
)

// max is how much of the patch the next read takes.
func (p *patchPager) max() int {
	switch p.stage {
	case pageFirst:
		return firstPatchPage
	case pageRest:
		return shownPatch
	}
	return wholePatch
}

// sides bounds the whole files read to unfold the lines around changes.
func (p *patchPager) sides() int {
	if p.stage == pageWhole {
		return wholePatch
	}
	return 0
}

// more reports whether res, a first page cut short, is read again for the
// rest; it moves on to that read.
func (p *patchPager) more(res session.PatchResult) bool {
	if p.stage == pageFirst && res.Err == nil && res.Truncated {
		p.stage = pageRest
		return true
	}
	return false
}

// sizeText is a limit as the notices say it.
func sizeText(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// cut reports a patch shown only in part.
func cut(res session.PatchResult) bool {
	return res.Truncated || res.Parsed != nil && res.Parsed.Cut
}

// pagerNotice says what of the patch is shown, with the button that reads
// all of it; whole runs that read. staging, when set, is said of a part.
func (a *App) pagerNotice(c *ui.Context, p *patchPager, res session.PatchResult, loading bool, staging string, whole func()) {
	th := c.Theme()
	var msg string
	offer := false
	switch {
	case cut(res) && loading:
		msg = "Showing the first " + sizeText(firstPatchPage) + " while the rest is read"
	case cut(res):
		msg = "Only the first " + sizeText(p.max()) + " of this patch are shown" + staging
		offer = p.stage != pageWhole
	case res.SidesTooLarge && !loading:
		msg = "The file is too large to unfold the lines around the changes"
		offer = p.stage != pageWhole
	default:
		return
	}
	ui.Row(c).Padding(4, 12).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Text(c, msg).FontSize(12).TextColor(th.Warning).Grow(1)
		if offer && ui.Button(c, "Load Whole Patch").Disabled(loading).Clicked() {
			p.stage = pageWhole
			whole()
		}
	})
}
