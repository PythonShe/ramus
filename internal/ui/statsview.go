package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/stats"
	"ramus/internal/session"
	"ramus/locales"
)

// statsState is a tab's statistics view.
type statsState struct {
	branch   string // full ref name, "" for every branch and tag
	from, to string // YYYY-MM-DD, as typed
	asked    *session.StatsRequest
	// again is a count cut short (the view left, the repository opened
	// again), asked again as it was when shown next: the dates typed
	// since were not submitted.
	again *session.StatsRequest
	res   session.StatsResult
	// title and bars are res's chart over time, made once per result
	// rather than in every frame.
	title   string
	bars    []bar
	loading bool
	err     string
	authors tableState
	// mode shows activity, files by size or files by changes.
	mode    int
	sizes   treemapState
	changes treemapState
}

// maxBars bounds the bars of the over-time chart: weeks are grouped
// into months, quarters or years to stay under it.
const maxBars = 120

func (a *App) statsView(c *ui.Context, t *tab) {
	th := c.Theme()
	ss := &t.stats
	if ss.asked == nil && t.s.State.Refs != nil {
		if ss.again != nil {
			a.loadStats(t, *ss.again)
		} else {
			a.countStats(t)
		}
	}
	ui.Column(c).Fill().Children(func() {
		// It wraps rather than cut the modes off in a narrow window.
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).Wrap().AlignItems(ui.Center).Children(func() {
			a.statsBranchMenu(c, t)
			from := ui.TextInput(c, &ss.from).Placeholder(locales.StatsFromPlaceholder()).Label(locales.StatsFrom()).Width(fieldNarrow)
			to := ui.TextInput(c, &ss.to).Placeholder(locales.StatsToPlaceholder()).Label(locales.StatsTo()).Width(fieldNarrow)
			if from.Submitted() || to.Submitted() {
				a.countStats(t)
			}
			if ss.err != "" {
				ui.Text(c, ss.err).FontSize(textSmall).TextColor(th.Danger).SingleLine()
			}
			ui.Spacer(c)
			ui.Segmented(c, &ss.mode, locales.StatsActivity(), locales.StatsFilesBySize(), locales.StatsFilesByChanges())
			if r := ss.res; !r.Done && r.Total > 0 {
				ui.Text(c, locales.StatsReadCommits(r.Read, r.Total)).FontSize(textSmall).TextColor(th.TextMuted)
			}
		})
		switch ss.mode {
		case 1:
			a.loadTreemap(t, &ss.sizes, false)
			ui.Column(c).Padding(spaceM, insetX).Grow(1).Children(func() { a.treemapView(c, &ss.sizes, byteSize, locales.StatsReadingFileSizes()) })
			return
		case 2:
			a.loadTreemap(t, &ss.changes, true)
			ui.Column(c).Padding(spaceM, insetX).Grow(1).Children(func() {
				if d := ss.changes.data; d != nil && !ss.changes.loading && d.pw.Err == nil {
					what := locales.StatsChangesOver(d.pw.Commits)
					if d.pw.Limited {
						what = locales.StatsChangesOverLimited(d.pw.Commits)
					}
					ui.Text(c, what).FontSize(textSmall).TextColor(th.TextMuted).Padding(0, 0, spaceS, 0)
				}
				a.treemapView(c, &ss.changes, changeCount, locales.StatsCountingChanges())
			})
			return
		}
		r := ss.res
		switch {
		case r.Err != nil:
			errorMessage(c, errText(r.Err))
			return
		case r.Stats.Commits == 0 && !r.Done:
			loading(c, locales.StatsReadingCommits())
			return
		case r.Stats.Commits == 0:
			emptyState(c, locales.StatsNoCommits())
			return
		}
		st := r.Stats
		// The table outside the charts' inset: its cells pad their text to
		// it already.
		ui.Column(c).Padding(spaceM, 0).Gap(spaceXL).Grow(1).Children(func() {
			ui.Column(c).Padding(0, insetX).Gap(spaceXL).FillWidth().Children(func() {
				statsCharts(c, st, ss.title, ss.bars)
			})
			a.authorTable(c, t, st)
		})
	})
}

// statsCharts shows the counts of commits and the charts of when they
// were made, title and bars the one over time.
func statsCharts(c *ui.Context, st stats.Stats, title string, bars []bar) {
	th := c.Theme()
	ui.Row(c).Gap(space3XL).AlignItems(ui.End).Children(func() {
		ui.Text(c, strconv.Itoa(st.Commits)).FontSize(textDisplay).Bold()
		ui.Text(c, locales.StatsCommitsBy(st.Commits, len(st.Authors),
			st.First.Format("2006-01-02"), st.Last.Format("2006-01-02"))).TextColor(th.TextMuted)
	})
	barChart(c, title, bars, 140)
	ui.Row(c).Gap(space3XL).FillWidth().Children(func() {
		ui.Column(c).Grow(1).Children(func() {
			barChart(c, locales.StatsByWeekday(), weekdayBars(st), 100)
		})
		ui.Column(c).Grow(2).Children(func() {
			barChart(c, locales.StatsByHour(), hourBars(st), 100)
		})
	})
}

// countStats asks for the counts the controls name.
func (a *App) countStats(t *tab) {
	ss := &t.stats
	req := session.StatsRequest{Branch: ss.branch}
	var err error
	if req.Since, err = parseDay(ss.from); err == nil {
		req.Until, err = parseDay(ss.to)
		if !req.Until.IsZero() {
			// The day typed is counted whole.
			req.Until = req.Until.AddDate(0, 0, 1)
		}
	}
	if err != nil {
		ss.err = errText(err)
		return
	}
	a.loadStats(t, req)
}

// loadStats asks for the counts of req.
func (a *App) loadStats(t *tab, req session.StatsRequest) {
	ss := &t.stats
	ss.err, ss.asked, ss.again, ss.loading = "", &req, nil, true
	ss.res, ss.title, ss.bars = session.StatsResult{Req: req}, "", nil
	now := a.Now()
	t.s.LoadStats(req, func(r session.StatsResult) {
		if reopened(r.Err) {
			// Counted again as shown next, for the same request.
			ss.asked, ss.again, ss.loading = nil, &req, false
			return
		}
		ss.res = r
		ss.title, ss.bars = overTime(r.Stats.Weeks, now)
		ss.loading = !r.Done
	})
}

// loadTreemap reads a treemap's files for the request the controls name,
// when it was read for another.
func (a *App) loadTreemap(t *tab, ts *treemapState, changes bool) {
	if ss := &t.stats; ss.asked != nil {
		req := *ss.asked
		if !changes {
			// Sizes are of the branch's tree, whatever the dates.
			req = session.StatsRequest{Branch: req.Branch}
		}
		if ts.data != nil && ts.For == req || ts.loading {
			return
		}
		asked := new(session.StatsRequest)
		*asked = req
		ts.loading, ts.For, ts.path, ts.reading = true, req, nil, asked
		done := func(pw session.PathWeights) {
			if reopened(pw.Err) {
				if ts.reading == asked {
					*ts = treemapState{} // read again as shown next
				}
				return
			}
			// The tree is built off the main thread too: a large
			// repository has hundreds of thousands of paths.
			update := a.Update
			a.goWork(t, func() {
				d := newTreemapData(pw)
				update(func() {
					// The view may have been left and shown again meanwhile,
					// and another read asked for.
					if ts.reading == asked {
						ts.data, ts.loading, ts.reading = d, false, nil
					}
				})
			})
		}
		if changes {
			t.s.ChangeCounts(req, done)
		} else {
			t.s.FileSizes(req.Branch, done)
		}
	}
}

// parseDay reads a day typed as YYYY-MM-DD, in local time; empty is no
// bound.
func parseDay(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	d, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, errors.New(locales.StatsNotADay(strconv.Quote(s)))
	}
	return d, nil
}

func (a *App) statsBranchMenu(c *ui.Context, t *tab) {
	ss := &t.stats
	label := locales.StatsAllBranches()
	if ss.branch != "" {
		label = redact.Control(strings.TrimPrefix(ss.branch, "refs/heads/"))
	}
	givesWay(ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
		// Items by the ref's full name: two names cleaned for showing may
		// read alike, and branches come and go while the menu is open.
		if m.Item(locales.StatsAllBranches()).ID("all").Chosen() {
			ss.branch = ""
			a.countStats(t)
		}
		list := t.s.State.Refs
		if list == nil {
			return
		}
		n := 0
		for i := range list.Len() {
			name := list.Name(i)
			if !strings.HasPrefix(name, "refs/heads/") {
				continue
			}
			if n++; n > sideLimit {
				break
			}
			if m.Item(redact.Control(strings.TrimPrefix(name, "refs/heads/"))).ID(name).Chosen() {
				ss.branch = name
				a.countStats(t)
			}
		}
	}))
}

// bar is one bar of a chart and what its tooltip says.
type bar struct {
	label string
	n     int
}

// futureSlack is how far past now a commit's date still counts where it
// says: a clock a little ahead is common; a date centuries out is a
// broken or hostile commit, whose week the chart counts at this bound
// rather than stretch itself out to it.
const futureSlack = 366 * 24 * time.Hour

// overTime groups the weeks into as few periods as keep the bars under
// maxBars, with the periods no one committed in shown empty; years are
// grouped by several when even they are too many. Its work is bounded
// by the weeks and maxBars, whatever the dates.
func overTime(weeks []stats.Week, now time.Time) (string, []bar) {
	if len(weeks) == 0 {
		return "", nil
	}
	limit := weekOf(now.Add(futureSlack))
	at := func(w stats.Week) int64 { return min(weekOf(w.Start), limit) }
	first, last := at(weeks[0]), at(weeks[len(weeks)-1])
	// A period is counted from an epoch; of is the one a week falls in.
	type period struct {
		title string
		of    func(week int64) int64
		label func(p int64) string
	}
	monthOf := func(week int64) int64 {
		t := weekStart(week)
		return int64(t.Year())*12 + int64(t.Month()-1)
	}
	periods := []period{
		{locales.StatsPerWeek(), func(w int64) int64 { return w },
			func(p int64) string { return locales.StatsWeekOf(weekStart(p).Format("2006-01-02")) }},
		{locales.StatsPerMonth(), monthOf, func(p int64) string {
			y := floorDiv(p, 12)
			return locales.StatsMonth(time.Date(int(y), time.Month(p-y*12+1), 1, 0, 0, 0, 0, time.UTC))
		}},
		{locales.StatsPerQuarter(), func(w int64) int64 { return floorDiv(monthOf(w), 3) }, func(p int64) string {
			y := floorDiv(p, 4)
			return locales.StatsQuarter(int(p-y*4)+1, int(y))
		}},
		{locales.StatsPerYear(), func(w int64) int64 { return floorDiv(monthOf(w), 12) },
			func(p int64) string { return strconv.FormatInt(p, 10) }},
	}
	for k, p := range periods {
		from, to := p.of(first), p.of(last)
		n, step := to-from+1, int64(1)
		if n > maxBars {
			if k < len(periods)-1 {
				continue
			}
			step = (n + maxBars - 1) / maxBars
		}
		bars := make([]bar, (n+step-1)/step)
		for i := range bars {
			lo := from + int64(i)*step
			bars[i].label = p.label(lo)
			if hi := min(lo+step-1, to); hi > lo {
				bars[i].label = locales.StatsRange(p.label(lo), p.label(hi))
			}
		}
		for _, w := range weeks {
			bars[(p.of(at(w))-from)/step].n += w.Commits
		}
		return p.title, bars
	}
	return "", nil
}

// weekOf is the week t falls in, counted from the Monday 1970-01-05, as
// stats.Week starts them; weekStart is its first day.
func weekOf(t time.Time) int64 { return floorDiv(floorDiv(t.Unix(), 86400)-4, 7) }

func weekStart(week int64) time.Time { return time.Unix((week*7+4)*86400, 0).UTC() }

// floorDiv is a/b rounded down, for dates before 1970 too.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func weekdayBars(st stats.Stats) []bar {
	// Monday first, as the weeks are.
	var out []bar
	for i := range 7 {
		d := (i + 1) % 7
		out = append(out, bar{locales.StatsWeekday(weekdayDate(time.Weekday(d))), st.Weekday[d]})
	}
	return out
}

// weekdayDate is a day that falls on d, for the catalog to name it.
func weekdayDate(d time.Weekday) time.Time {
	// 2024-01-07 was a Sunday.
	return time.Date(2024, 1, 7+int(d), 12, 0, 0, 0, time.UTC)
}

func hourBars(st stats.Stats) []bar {
	var out []bar
	for h := range 24 {
		out = append(out, bar{locales.StatsRange(fmt.Sprintf("%02d:00", h), fmt.Sprintf("%02d:59", h)), st.Hour[h]})
	}
	return out
}

// barChart draws one series of bars on a baseline, the title naming it;
// each bar's column, taller than the bar, shows its count on hover.
func barChart(c *ui.Context, title string, bars []bar, height float32) {
	th := c.Theme()
	most := 1
	for _, b := range bars {
		most = max(most, b.n)
	}
	ui.Column(c).Gap(spaceS).FillWidth().Children(func() {
		ui.Row(c).Gap(spaceM).Children(func() {
			ui.Text(c, title).FontSize(textSmall).Bold()
			ui.Text(c, locales.StatsMost(most)).FontSize(textSmall).TextColor(th.TextMuted)
		})
		ui.Row(c).Gap(space2XS).Height(height).FillWidth().AlignItems(ui.End).Children(func() {
			for i, b := range bars {
				tip := locales.StatsBarTip(b.label, b.n)
				ui.Column(c.Key(i)).Grow(1).FillHeight().Justify(ui.End).Tooltip(tip).Label(tip).Role(ui.RoleImage).Children(func() {
					if b.n > 0 {
						h := max(2, height*float32(b.n)/float32(most))
						ui.Box(c).Height(h).FillWidth().Background(th.Accent).Radius(min(radiusSmall, h/2), min(radiusSmall, h/2), 0, 0)
					}
				})
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(th.TextMuted.Alpha(0.3))
		if len(bars) > 1 {
			ui.Row(c).FillWidth().Children(func() {
				ui.Text(c, bars[0].label).FontSize(textCaption).TextColor(th.TextMuted).SingleLine()
				ui.Spacer(c)
				ui.Text(c, bars[len(bars)-1].label).FontSize(textCaption).TextColor(th.TextMuted).SingleLine()
			})
		}
	})
}

func (a *App) authorTable(c *ui.Context, t *tab, st stats.Stats) {
	ss := &t.stats
	authors := st.Authors
	ss.authors.Label = func(i int) string {
		au := authors[i]
		return locales.StatsNameCommits(redact.Control(au.Name), au.Commits)
	}
	cols := []ui.TableColumn{
		{Title: locales.StatsPerson(), ID: "person", MinWidth: 120},
		{Title: locales.StatsCommits(), ID: "commits", Width: 92, MinWidth: 50, Align: ui.End},
		{Title: locales.StatsShare(), ID: "share", Width: 76, MinWidth: 50, Align: ui.End},
		{Title: locales.StatsActive(), ID: "active", Width: 210, MinWidth: 80},
	}
	a.savedTable(c, "authors", &ss.authors, cols, len(authors), func(i, col int) {
		au := authors[i]
		switch col {
		case 0:
			ui.Text(c, redact.Control(au.Name)+" <"+redact.Control(au.Email)+">").SingleLine().Selectable()
		case 1:
			ui.Text(c, strconv.Itoa(au.Commits))
		case 2:
			ui.Text(c, fmt.Sprintf("%.1f%%", 100*float64(au.Commits)/float64(st.Commits))).TextColor(ss.authors.ink(c, i).Muted)
		case 3:
			mutedCell(c, ss.authors.ink(c, i), locales.StatsRange(time.Unix(au.First, 0).Format("2006-01-02"), time.Unix(au.Last, 0).Format("2006-01-02")))
		}
	}).Label(locales.StatsAuthors()).Grow(1).MinHeight(160)
}
