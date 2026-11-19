package ui

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"ramus/internal/forge"
	"ramus/internal/git/redact"
	"ramus/locales"
)

// What a pull request's page shows beside its files and what was said:
// whether it can be merged, its labels and reviewers, the checks of its
// latest commit, and what happened to it.

// mergeText says whether the host would merge r now, and in what color;
// "" for one it has not worked out, and for a draft, which says so
// already.
func mergeText(th *ui.Theme, r *forge.Review) (string, ui.Color) {
	base := redact.Control(r.Base)
	switch r.Mergeable {
	case forge.MergeReady:
		return locales.PrsMergeable("ready", base), th.Success
	case forge.MergeConflicts:
		return locales.PrsMergeable("conflicts", base), th.Danger
	case forge.MergeBehind:
		return locales.PrsMergeable("behind", base), th.Warning
	case forge.MergeBlocked:
		return locales.PrsMergeable("blocked", base), th.TextMuted
	case forge.MergeUnstable:
		return locales.PrsMergeable("unstable", base), th.Warning
	}
	return "", ui.Color{}
}

// labelColor is a label's color, as six hex digits; ok is false for none.
func labelColor(hex string) (col ui.Color, ok bool) {
	if len(hex) != 6 {
		return ui.Color{}, false
	}
	n, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return ui.Color{}, false
	}
	return ui.RGB(uint8(n>>16), uint8(n>>8), uint8(n)), true
}

// labelChip shows a label of a pull request: its name on a wash of its
// color, with a dot of the color itself, which a wash alone loses for
// the pale ones.
func labelChip(c *ui.Context, l forge.Label) {
	th := c.Theme()
	name := redact.Control(l.Name)
	col, ok := labelColor(l.Color)
	if !ok {
		col = th.TextMuted
	}
	ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Padding(1, spaceS).Radius(radiusSmall).Background(col.Alpha(0.16)).Tooltip(name).Children(func() {
		ui.Box(c).Width(labelDot).Height(labelDot).Radius(labelDot / 2).Background(col)
		ui.Text(c, name).FontSize(textCaption).SingleLine().TruncateMiddle().MaxWidth(refChipMax)
	})
}

// labelDot is the side of the dot of a label's color.
const labelDot = 7

// reviewerMark is the icon of a reviewer's latest verdict, its color and
// the word a message selects it by.
func reviewerMark(th *ui.Theme, s forge.ReviewState) (*ui.SVG, ui.Color, string) {
	g := glyphs()
	switch s {
	case forge.ReviewApproved:
		return g.check, th.Success, "approved"
	case forge.ReviewChangesRequested:
		return g.x, th.Danger, "changes"
	case forge.ReviewCommented:
		return g.dot, th.TextMuted, "commented"
	case forge.ReviewDismissed:
		return g.dot, th.TextMuted, "dismissed"
	}
	return g.history, th.Warning, "awaited"
}

// prPeople is the row under a pull request's title of its labels, its
// reviewers, each with their latest verdict, and those it is assigned
// to; nothing when it has none. A pane with room to spare has them
// beside the tabs instead, on one line (inline), and beside the
// conversation (prAside).
func prPeople(c *ui.Context, r *forge.Review, inline bool) {
	if len(r.Labels) == 0 && len(r.Reviewers) == 0 && len(r.Assignees) == 0 {
		return
	}
	th := c.Theme()
	caption := func(s string) {
		ui.Text(c, s).FontSize(textSmall).TextColor(th.TextMuted).SingleLine()
	}
	row := ui.Row(c).Gap(spaceM).AlignItems(ui.Center)
	if inline {
		// What does not fit is cut: a second line would move the tabs.
		row.Shrink(1).MinWidth(0).Clip()
	} else {
		row.Padding(0, insetX, spaceXS, insetX).Wrap().FillWidth()
	}
	row.Children(func() {
		if len(r.Reviewers) > 0 {
			ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Label(locales.PrsReviewers()).Children(func() {
				caption(locales.PrsReviewers())
				for _, rv := range r.Reviewers {
					icon, col, word := reviewerMark(th, rv.State)
					says := locales.PrsReviewer(redact.Control(rv.Name), word)
					// A team may have the name of a user who reviewed.
					ui.Row(c.Key(forge.Reviewer{Name: rv.Name, Team: rv.Team})).Gap(space2XS).AlignItems(ui.Center).Label(says).Tooltip(says).Children(func() {
						ui.Icon(c, icon).FontSize(iconSmall).TextColor(col)
						ui.Text(c, redact.Control(rv.Name)).FontSize(textSmall).SingleLine()
					})
				}
			})
		}
		if len(r.Assignees) > 0 {
			ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
				caption(locales.PrsAssignees())
				ui.Text(c, redact.Control(strings.Join(r.Assignees, ", "))).FontSize(textSmall).SingleLine()
			})
		}
		if len(r.Labels) > 0 {
			labels := ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Label(locales.PrsLabels())
			if !inline {
				labels.Wrap()
			}
			labels.Children(func() {
				for _, l := range r.Labels {
					labelChip(c, l)
				}
			})
		}
	})
}

// checkRank orders the checks: what needs looking at first.
func checkRank(s forge.CheckState) int {
	switch s {
	case forge.CheckFailed:
		return 0
	case forge.CheckRunning:
		return 1
	case forge.CheckWaiting:
		return 2
	case forge.CheckCancelled:
		return 3
	case forge.CheckPassed:
		return 4
	}
	return 5
}

// sortChecks orders list as the page lists it: the failed first, then
// those running and waiting; among those that stand alike the required
// first, then each by its group and name.
func sortChecks(list []forge.Check) []forge.Check {
	out := slices.Clone(list)
	required := func(c forge.Check) int {
		if c.Required {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(out, func(x, y forge.Check) int {
		return cmp.Or(cmp.Compare(checkRank(x.State), checkRank(y.State)), cmp.Compare(required(x), required(y)),
			cmp.Compare(strings.ToLower(x.Group), strings.ToLower(y.Group)), cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)))
	})
	return out
}

// checkMark is the icon of where a check stands, its color and the word
// a message selects it by.
func checkMark(k ink, s forge.CheckState) (*ui.SVG, ui.Color, string) {
	g := glyphs()
	switch s {
	case forge.CheckPassed:
		return g.check, k.Success, "passed"
	case forge.CheckFailed:
		return g.x, k.Danger, "failed"
	case forge.CheckRunning:
		return g.dot, k.Warning, "running"
	case forge.CheckWaiting:
		return g.history, k.Muted, "waiting"
	case forge.CheckCancelled:
		return g.x, k.Muted, "cancelled"
	}
	return g.dot, k.Muted, "skipped"
}

// checkTook is how long a check ran, or has run so far; "" for one that
// did not start.
func checkTook(ch forge.Check, now time.Time) string {
	if ch.Started.IsZero() {
		return ""
	}
	end := ch.Ended
	if end.IsZero() {
		if ch.State != forge.CheckRunning {
			return ""
		}
		end = now
	}
	// One skipped ends as it starts.
	d := end.Sub(ch.Started).Round(time.Second)
	if d <= 0 {
		return ""
	}
	return d.String()
}

// prChecks lists the checks of the pull request's latest commit; one
// opens its page in the browser.
func (a *App) prChecks(c *ui.Context, rv *prReview) {
	th := c.Theme()
	list := rv.checks
	if len(list) == 0 {
		emptyState(c, locales.PrsNoChecks())
		return
	}
	now := a.Now()
	open := ""
	ui.Column(c).Fill().Children(func() {
		if rv.r.ChecksMore {
			ui.Text(c, locales.PrsChecksMore(kindName(rv.repo.Kind))).FontSize(textSmall).TextColor(th.Warning).Padding(0, insetX, spaceS, insetX)
		}
		rv.checkList.Key = func(i int) any { return [3]string{list[i].Group, list[i].Name, list[i].URL} }
		ui.List(c, &rv.checkList, len(list), func(i int) {
			ch := list[i]
			k := plainInk(th)
			icon, col, word := checkMark(k, ch.State)
			row := ui.Row(c).Height(rowHeight).Padding(0, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().
				Label(locales.PrsCheckRow(redact.Control(ch.Name), locales.PrsCheckState(word)))
			if ch.URL != "" {
				row.Role(ui.RoleButton).Cursor(ui.CursorPointer).Tooltip(ch.URL)
				row.ContextMenu(func(m *ui.Menu) {
					if m.Item(locales.PrsOpenInBrowser()).Chosen() {
						open = ch.URL
					}
				})
				if row.Clicked() {
					open = ch.URL
				}
			}
			row.Children(func() {
				says := locales.PrsCheckState(word)
				ui.Icon(c, icon).FontSize(iconMedium).TextColor(col).Label(says).Tooltip(says)
				ui.Text(c, redact.Control(ch.Name)).SingleLine().TruncateMiddle().Shrink(1)
				if ch.Group != "" {
					ui.Text(c, redact.Control(ch.Group)).FontSize(textSmall).TextColor(k.Muted).SingleLine().Shrink(2)
				}
				ui.Spacer(c)
				if ch.Required {
					ui.Text(c, locales.PrsCheckRequired()).FontSize(textSmall).TextColor(k.Muted).SingleLine()
				}
				// Its room is kept for one that did not run, so that what is
				// before it lines up.
				ui.Text(c, checkTook(ch, now)).FontSize(textSmall).TextColor(k.Muted).SingleLine().Width(checkTookW).TextAlign(ui.End)
			})
		}).Label(locales.PrsChecksLabel()).Grow(1)
	})
	if open != "" {
		a.openURL = open
	}
}

// checkTookW is the room of a check's time, so that the times line up.
const checkTookW = 72

// eventText says what happened, after who did it.
func eventText(e *forge.Event) string {
	switch e.Kind {
	case forge.EventCommits:
		return locales.PrsEventCommits(len(e.Commits))
	case forge.EventForcePushed:
		return locales.PrsEventForcePushed(redact.Control(e.From), redact.Control(e.Subject))
	case forge.EventMerged:
		return locales.PrsEventState("merged")
	case forge.EventClosed:
		return locales.PrsEventState("closed")
	case forge.EventReopened:
		return locales.PrsEventState("reopened")
	case forge.EventReady:
		return locales.PrsEventState("ready")
	case forge.EventDrafted:
		return locales.PrsEventState("draft")
	case forge.EventReviewRequested:
		return locales.PrsEventReviewRequested(redact.Control(e.Subject))
	case forge.EventLabeled:
		return locales.PrsEventLabeled(len(e.Labels), redact.Control(strings.Join(e.Labels, ", ")))
	case forge.EventUnlabeled:
		return locales.PrsEventUnlabeled(len(e.Labels), redact.Control(strings.Join(e.Labels, ", ")))
	case forge.EventRenamed:
		return locales.PrsEventRenamed(redact.Control(e.From), redact.Control(e.Subject))
	}
	return ""
}

// eventCommits lists the commits an event added, under it.
func eventCommits(c *ui.Context, e *forge.Event) {
	th := c.Theme()
	for _, cm := range e.Commits {
		ui.Row(c.Key(cm.ID)).Gap(spaceM).Children(func() {
			ui.Text(c, redact.Control(cm.ID)).Font("monospace").FontSize(textSmall).TextColor(th.TextMuted).SingleLine()
			ui.Text(c, redact.Control(cm.Subject)).FontSize(textSmall).SingleLine().Shrink(1).Selectable()
		})
	}
}

// webLink reports an address a link in a comment may send the browser
// to: a page, with no user name in it.
func webLink(dest string) bool {
	u, err := url.Parse(dest)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.Opaque == "" && u.Hostname() != ""
}

// markdownOptions are how the review shows what was written in Markdown:
// links open in the browser, and on GitHub a mention and a reference to
// an issue or a pull request are links to their pages. Pictures are not
// fetched: they show as their descriptions.
func (a *App) markdownOptions(rv *prReview) *markdown.Options {
	if rv.md != nil {
		return rv.md
	}
	repo := rv.repo
	site := strings.TrimSuffix(repo.Web(), "/"+repo.Path)
	rv.md = &markdown.Options{
		Theme: commentTheme(),
		OnLink: func(dest string) {
			if webLink(dest) {
				a.openURL = dest
			}
		},
		Resolve: func(in parser.Inline) (string, bool) {
			if repo.Kind != forge.GitHub {
				return "", false
			}
			switch in.Kind {
			case parser.Mention:
				// A team's page is under its organization's.
				if login := in.Login(); login != "" && !strings.Contains(login, "/") {
					return site + "/" + login, true
				}
			case parser.Reference:
				// GitHub sends an issue's address to the pull request of
				// that number.
				if path, n := in.Ref(); n > 0 {
					if path == "" {
						path = repo.Path
					}
					return site + "/" + path + "/issues/" + strconv.Itoa(n), true
				}
			}
			return "", false
		},
	}
	return rv.md
}
