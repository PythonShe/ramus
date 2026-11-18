package ui

import (
	"context"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"ramus/internal/forge"
	"ramus/internal/git/redact"
	"ramus/locales"
)

const (
	// convMaxW is the measure of a conversation: lines the width of a
	// wide window are not read, they are followed with a finger. What a
	// pane has past it goes to the details beside (prAside) and to the
	// margins, the conversation in the middle of its room.
	convMaxW = 860
	// convAsideMin is the pane from which the details stand beside the
	// conversation, and asideMinW to asideMaxW the room they take, a
	// share (asideShare) of the pane's.
	convAsideMin         = 900
	asideMinW, asideMaxW = 260, 340
	asideShare           = 0.24
	// asideChecks bounds the checks listed beside the conversation: the
	// ones to look at, the others counted.
	asideChecks = 6
	// convMark is the picture of who wrote a comment, and the room of
	// the icon of what happened between two.
	convMark = 20
)

// commentTheme is how a comment's Markdown looks: text of the size of
// the app's, with headings that head a comment rather than a page.
func commentTheme() *markdown.Theme {
	th := &markdown.Theme{FontSize: noteBodySize}
	for i, size := range [6]float32{1.3, 1.2, 1.1, 1, 1, 1} {
		th.Headings[i] = markdown.HeadingStyle{Size: size, Top: 1, Bottom: 0.4}
	}
	return th
}

// parts are the parts of body, a comment as shown, read once.
func (rv *prReview) parts(body string) []*commentPart {
	if p, ok := rv.bodies[body]; ok {
		return p
	}
	if rv.bodies == nil {
		rv.bodies = map[string][]*commentPart{}
	}
	p := commentParts(body, locales.PrsImage())
	rv.bodies[body] = p
	return p
}

// commentBody shows body, a description or a comment, as its Markdown
// reads; id is the entry it is in and key what tells it from the others
// there, for the folds it has.
func (a *App) commentBody(c *ui.Context, rv *prReview, id, key, body string) {
	a.commentPartsView(c, rv, id, key, rv.parts(body))
}

func (a *App) commentPartsView(c *ui.Context, rv *prReview, id, key string, parts []*commentPart) {
	ui.Column(c).FillWidth().Gap(spaceM).Children(func() {
		for i, p := range parts {
			k := key + "/" + strconv.Itoa(i)
			if !p.fold {
				markdown.View(c.Key(k), p.md, a.markdownOptions(rv)).FillWidth()
				continue
			}
			open := rv.folds[id][k]
			if open == nil {
				if rv.folds == nil {
					rv.folds = map[string]map[string]*bool{}
				}
				if rv.folds[id] == nil {
					rv.folds[id] = map[string]*bool{}
				}
				open = new(bool)
				*open = p.open
				rv.folds[id][k] = open
			}
			summary := p.summary
			if summary == "" {
				summary = locales.PrsFold()
			}
			ui.Collapsible(c.Key(k), summary, open, func() { a.commentPartsView(c, rv, id, k, p.parts) }).FillWidth()
		}
	})
}

// card reports an entry with words of its own, shown as a card; the
// others, what happened and verdicts without a word, are a line.
func (e *prEvent) card(first bool) bool {
	return first || e.thread != nil || e.event == nil && strings.TrimSpace(e.body) != ""
}

// mark is the icon of an entry that is a line, and its color.
func (e *prEvent) mark(th *ui.Theme) (*ui.SVG, ui.Color) {
	g := glyphs()
	if e.event == nil {
		switch e.state {
		case forge.ReviewApproved:
			return g.check, th.Success
		case forge.ReviewChangesRequested:
			return g.x, th.Danger
		}
		return g.eye, th.TextMuted
	}
	switch e.event.Kind {
	case forge.EventCommits:
		return g.history, th.TextMuted
	case forge.EventForcePushed:
		return g.arrowUp, th.TextMuted
	case forge.EventMerged:
		return g.pullRequest, th.Success
	case forge.EventClosed:
		return g.x, th.Danger
	case forge.EventReopened:
		return g.refresh, th.Success
	case forge.EventReady:
		return g.eye, th.TextMuted
	case forge.EventReviewRequested:
		return g.personAdd, th.TextMuted
	case forge.EventLabeled, forge.EventUnlabeled:
		return g.tag, th.TextMuted
	}
	return g.dot, th.TextMuted
}

// whatColor colors what an entry says it is: a verdict in its color.
func (e *prEvent) whatColor(th *ui.Theme) ui.Color {
	switch e.state {
	case forge.ReviewApproved:
		return th.Success
	case forge.ReviewChangesRequested:
		return th.Danger
	}
	return th.TextMuted
}

// asideW is the room of the details beside the conversation: 0 in a
// pane too narrow for both, and for a pull request with none.
func (rv *prReview) asideW() float32 {
	r := &rv.r
	if rv.paneW < convAsideMin || len(r.Labels)+len(r.Reviewers)+len(r.Assignees)+len(rv.checks) == 0 {
		return 0
	}
	return min(max(rv.paneW*asideShare, asideMinW), asideMaxW)
}

func (a *App) prConversation(c *ui.Context, t *tab, rv *prReview) {
	th := c.Theme()
	ev := rv.timeline()
	side := rv.asideW()
	// measured holds what is read to its measure, in the middle of the
	// room it has.
	measured := func(pad [4]float32, gap float32, fn func()) ui.Element {
		return ui.Column(c).FillWidth().Padding(pad[0], pad[1], pad[2], pad[3]).AlignItems(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(convMaxW).Gap(gap).Children(fn)
		})
	}
	// The line the conversation scrolls under, and the one between it
	// and the details starts from.
	ui.Box(c).Height(1).FillWidth().Background(th.Border)
	// Grow, not Fill: the page's header is above it, and all the height
	// would push the comment being written out of the window.
	ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).FillHeight().Children(func() {
			rv.conv.Label = func(i int) string { return ev[i].author + ", " + ev[i].what }
			ui.List(c, &rv.conv, len(ev), func(i int) {
				e := &ev[i]
				top := float32(spaceS)
				if i == 0 {
					top = spaceM
				}
				measured([4]float32{top, insetX, spaceS, insetX}, 0, func() {
					if e.card(i == 0) {
						a.convCard(c, t, rv, e)
					} else {
						a.convLine(c, e)
					}
				})
			}).Label(locales.PrsConversationLabel()).Grow(1).MinHeight(0)
			ui.Box(c).Height(1).FillWidth().Background(th.Border)
			measured([4]float32{spaceM, insetX, spaceM, insetX}, spaceS, func() {
				ui.TextArea(c, &rv.comment).Label(locales.PrsCommentPlaceholder()).Placeholder(locales.PrsCommentPlaceholder()).Lines(2, 8).FillWidth()
				body := strings.TrimSpace(rv.comment)
				ui.Row(c).FillWidth().Children(func() {
					ui.Spacer(c)
					if ui.Button(c, locales.PrsAddComment()).Disabled(body == "" || rv.busy()).Clicked() {
						a.prSend(t, rv, func(ctx context.Context, cl *forge.CLI) error { return cl.AddComment(ctx, rv.repo, rv.n, body) },
							func() { rv.comment = "" })
					}
				})
			}).Shrink(0)
		})
		if side > 0 {
			ui.Box(c).Width(1).FillHeight().Background(th.Border)
			a.prAside(c, t, rv, side)
		}
	})
}

// prAside is what a wide pane has beside the conversation, where a
// narrow one has a row under the title: who reviews the pull request,
// each with their latest verdict, whom it is assigned to, its labels,
// and the checks to look at.
func (a *App) prAside(c *ui.Context, t *tab, rv *prReview, w float32) {
	th := c.Theme()
	r := &rv.r
	open := ""
	section := func(title string, fn func()) {
		ui.Column(c).FillWidth().Gap(spaceS).Children(func() {
			ui.Text(c, title).FontSize(textSmall).Bold().TextColor(th.TextMuted).SingleLine()
			fn()
		})
	}
	person := func(name string, tail func()) {
		a.avatar(c, t, name, name, asideMark)
		ui.Text(c, name).SingleLine().TruncateMiddle().Shrink(1)
		ui.Spacer(c)
		if tail != nil {
			tail()
		}
	}
	ui.Scroll(c).Width(w).Shrink(0).FillHeight().Children(func() {
		ui.Column(c).FillWidth().Padding(spaceM, insetX, spaceL, insetX).Gap(spaceL + spaceXS).Children(func() {
			if len(r.Reviewers) > 0 {
				section(locales.PrsReviewers(), func() {
					for _, who := range r.Reviewers {
						icon, col, word := reviewerMark(th, who.State)
						name := redact.Control(who.Name)
						says := locales.PrsReviewer(name, word)
						// A team may have the name of a user who reviewed.
						ui.Row(c.Key(forge.Reviewer{Name: who.Name, Team: who.Team})).FillWidth().Gap(spaceM).AlignItems(ui.Center).
							Label(says).Tooltip(says).Children(func() {
							person(name, func() { ui.Icon(c, icon).FontSize(iconMedium).TextColor(col) })
						})
					}
				})
			}
			if len(r.Assignees) > 0 {
				section(locales.PrsAssignees(), func() {
					for _, who := range r.Assignees {
						ui.Row(c.Key(who)).FillWidth().Gap(spaceM).AlignItems(ui.Center).Children(func() { person(redact.Control(who), nil) })
					}
				})
			}
			if len(r.Labels) > 0 {
				section(locales.PrsLabels(), func() {
					ui.Row(c).FillWidth().Gap(spaceXS).Wrap().AlignItems(ui.Center).Children(func() {
						for _, l := range r.Labels {
							labelChip(c, l)
						}
					})
				})
			}
			if len(rv.checks) > 0 {
				section(locales.PrsChecksLabel(), func() { open = a.asideChecks(c, rv) })
			}
		})
	})
	if open != "" {
		a.openURL = open
	}
}

// asideMark is the picture of a person beside the conversation.
const asideMark = 18

// asideChecks lists, beside the conversation, the checks that did not
// pass, and counts the others; the counts lead to the page of them all.
// It returns the page of the check that was clicked.
func (a *App) asideChecks(c *ui.Context, rv *prReview) (open string) {
	th := c.Theme()
	k := plainInk(th)
	line := func(key any, icon *ui.SVG, col ui.Color, text, says, tip string) ui.Element {
		row := ui.Row(c.Key(key)).FillWidth().Gap(spaceM).AlignItems(ui.Center).Label(says).Role(ui.RoleButton).Cursor(ui.CursorPointer)
		if tip != "" {
			row.Tooltip(tip)
		}
		return row.Children(func() {
			ui.Box(c).Size(asideMark, asideMark).Shrink(0).Center().Children(func() {
				ui.Icon(c, icon).FontSize(iconMedium).TextColor(col)
			})
			ui.Text(c, text).SingleLine().TruncateMiddle().Shrink(1)
		})
	}
	// They are sorted: those to look at first, then the passed.
	listed, passed, skipped := 0, 0, 0
	for i, ch := range rv.checks {
		switch rank := checkRank(ch.State); {
		case ch.State == forge.CheckPassed:
			passed++
			continue
		case rank > checkRank(forge.CheckPassed):
			skipped++
			continue
		case listed == asideChecks:
			continue
		}
		listed++
		icon, col, word := checkMark(k, ch.State)
		name := redact.Control(ch.Name)
		row := line(i, icon, col, name, locales.PrsCheckRow(name, locales.PrsCheckState(word)), ch.URL)
		if row.Clicked() {
			if ch.URL != "" {
				open = ch.URL
			} else {
				rv.page = 2
			}
		}
	}
	g := glyphs()
	// Each count leads to the page of all the checks.
	count := func(key string, icon *ui.SVG, col ui.Color, n int, says string) {
		if n > 0 && line(key, icon, col, says, says, "").Clicked() {
			rv.page = 2
		}
	}
	rest := len(rv.checks) - listed - passed - skipped
	count("rest", g.dot, k.Muted, rest, locales.PrsChecksRest(rest))
	count("passed", g.check, k.Success, passed, locales.PrsChecksPassed(passed))
	count("skipped", g.dot, k.Muted, skipped, locales.PrsChecksSkipped(skipped))
	return open
}

// convCard is an entry with words: who wrote it and when on a strip, and
// under it what they wrote, or the comments of a thread.
func (a *App) convCard(c *ui.Context, t *tab, rv *prReview, e *prEvent) {
	th := c.Theme()
	now := a.Now()
	ui.Column(c).FillWidth().Border(1, th.Border).Radius(th.Radius).Clip().Children(func() {
		ui.Row(c).FillWidth().Padding(spaceS, spaceL).Gap(spaceM).AlignItems(ui.Center).Background(th.Surface).Children(func() {
			a.avatar(c, t, e.author, e.author, convMark)
			ui.Text(c, e.author).Bold().FontSize(textSmall).SingleLine().Shrink(0)
			ui.Text(c, e.what).FontSize(textSmall).TextColor(e.whatColor(th)).SingleLine().TruncateMiddle().Shrink(1)
			ui.Spacer(c)
			ui.Text(c, ago(now, e.at)).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Shrink(0)
		})
		ui.Box(c).Height(1).FillWidth().Background(th.Border)
		ui.Column(c).FillWidth().Padding(spaceL).Gap(spaceL).Children(func() {
			if e.thread != nil {
				for n, cm := range e.thread.Comments {
					ui.Column(c.Key(cm.ID)).FillWidth().Gap(spaceXS).Children(func() {
						// The first is whose the card says it is.
						if n > 0 {
							commentHead(c, cm.Author, cm.At, now)
						}
						// One the host gave no id is told by its place.
						key := cm.ID
						if key == "" {
							key = strconv.Itoa(n)
						}
						a.commentBody(c, rv, e.id, key, shownBody(cm.Body))
					})
				}
				return
			}
			if body := shownBody(e.body); body != "" {
				a.commentBody(c, rv, e.id, "", body)
			} else {
				ui.Text(c, locales.PrsNoDescription()).FontSize(noteBodySize).TextColor(th.TextMuted)
			}
		})
	})
}

// convLine is an entry without words: an icon, who did what, and when,
// with the commits an event added under it.
func (a *App) convLine(c *ui.Context, e *prEvent) {
	th := c.Theme()
	icon, col := e.mark(th)
	// Its icon stands under the pictures of the cards around it.
	ui.Column(c).FillWidth().Padding(0, spaceL+1).Gap(space2XS).Children(func() {
		ui.Row(c).FillWidth().Gap(spaceM).AlignItems(ui.Start).Children(func() {
			ui.Box(c).Size(convMark, convMark).Shrink(0).Center().Children(func() {
				ui.Icon(c, icon).FontSize(iconMedium).TextColor(col)
			})
			ui.RichText(c,
				ui.Span{Text: e.author, Size: textSmall, Weight: 600, Color: th.Text},
				ui.Span{Text: " " + e.what, Size: textSmall, Color: e.whatColor(th)},
			).Grow(1).Shrink(1).Padding(space2XS, 0)
			ui.Text(c, ago(a.Now(), e.at)).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Shrink(0).Padding(space2XS, 0)
		})
		if e.event != nil && len(e.event.Commits) > 0 {
			ui.Column(c).FillWidth().Padding(0, 0, 0, convMark+spaceM).Children(func() { eventCommits(c, e.event) })
		}
	})
}
