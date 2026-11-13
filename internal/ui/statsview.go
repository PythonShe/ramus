package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/stats"
	"ramus/internal/session"
)

// statsState is a tab's statistics view.
type statsState struct {
	branch   string // full ref name, "" for every branch and tag
	from, to string // YYYY-MM-DD, as typed
	asked    *session.StatsRequest
	res      session.StatsResult
	loading  bool
	err      string
	authors  ui.ListState
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
		a.countStats(t)
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			a.statsBranchMenu(c, t)
			from := ui.TextInput(c, &ss.from).Placeholder("From YYYY-MM-DD").Label("From").Width(150)
			to := ui.TextInput(c, &ss.to).Placeholder("To YYYY-MM-DD").Label("To").Width(150)
			if from.Submitted() || to.Submitted() {
				a.countStats(t)
			}
			if ss.err != "" {
				ui.Text(c, ss.err).FontSize(12).TextColor(th.Danger).SingleLine()
			}
			ui.Spacer(c)
			ui.Segmented(c, &ss.mode, "Activity", "Files by Size", "Files by Changes")
			if r := ss.res; !r.Done && r.Total > 0 {
				ui.Text(c, fmt.Sprintf("Read %d of %d commits", r.Read, r.Total)).FontSize(12).TextColor(th.TextMuted)
			}
		})
		switch ss.mode {
		case 1:
			a.loadTreemap(t, &ss.sizes, false)
			ui.Column(c).Padding(8, 16).Grow(1).Children(func() { a.treemapView(c, &ss.sizes, byteSize) })
			return
		case 2:
			a.loadTreemap(t, &ss.changes, true)
			ui.Column(c).Padding(8, 16).Grow(1).Children(func() {
				if d := ss.changes.data; d != nil && !ss.changes.loading && d.pw.Err == nil {
					what := "Changes over " + commitCount(d.pw.Commits)
					if d.pw.Limited {
						what += ", the latest; choose a start date to go further back"
					}
					ui.Text(c, what).FontSize(12).TextColor(th.TextMuted).Padding(0, 0, 6, 0)
				}
				a.treemapView(c, &ss.changes, changeCount)
			})
			return
		}
		r := ss.res
		switch {
		case r.Err != nil:
			ui.Text(c, errText(r.Err)).TextColor(th.Danger).Padding(12).Selectable()
			return
		case r.Stats.Commits == 0 && !r.Done:
			ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
			return
		case r.Stats.Commits == 0:
			ui.Column(c).Fill().Center().Children(func() {
				ui.Text(c, "No commits in this range").TextColor(th.TextMuted)
			})
			return
		}
		st := r.Stats
		ui.Column(c).Padding(8, 16).Gap(16).Grow(1).Children(func() {
			ui.Row(c).Gap(24).AlignItems(ui.End).Children(func() {
				ui.Text(c, strconv.Itoa(st.Commits)).FontSize(28).Bold()
				ui.Text(c, fmt.Sprintf("commits by %s, %s to %s", people(len(st.Authors)),
					st.First.Format("2006-01-02"), st.Last.Format("2006-01-02"))).TextColor(th.TextMuted)
			})
			title, bars := overTime(st.Weeks)
			barChart(c, title, bars, 140)
			ui.Row(c).Gap(24).FillWidth().Children(func() {
				ui.Column(c).Grow(1).Children(func() {
					barChart(c, "By weekday", weekdayBars(st), 100)
				})
				ui.Column(c).Grow(2).Children(func() {
					barChart(c, "By hour of the author's day", hourBars(st), 100)
				})
			})
			a.authorTable(c, t, st)
		})
	})
}

func people(n int) string {
	if n == 1 {
		return "1 person"
	}
	return strconv.Itoa(n) + " people"
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
	ss.err, ss.asked, ss.loading = "", &req, true
	ss.res = session.StatsResult{Req: req}
	t.s.LoadStats(req, func(r session.StatsResult) {
		ss.res = r
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
		ts.loading, ts.For, ts.path = true, req, nil
		done := func(pw session.PathWeights) {
			// The tree is built off the main thread too: a large
			// repository has hundreds of thousands of paths.
			update := a.Update
			go func() {
				d := newTreemapData(pw)
				update(func() { ts.data, ts.loading = d, false })
			}()
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
		return time.Time{}, fmt.Errorf("%q is not a day like 2024-03-31", s)
	}
	return d, nil
}

func (a *App) statsBranchMenu(c *ui.Context, t *tab) {
	ss := &t.stats
	label := "All Branches and Tags"
	if ss.branch != "" {
		label = strings.TrimPrefix(ss.branch, "refs/heads/")
	}
	ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
		if m.Item("All Branches and Tags").Chosen() {
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
			if m.Item(strings.TrimPrefix(name, "refs/heads/")).Chosen() {
				ss.branch = name
				a.countStats(t)
			}
		}
	})
}

// bar is one bar of a chart and what its tooltip says.
type bar struct {
	label string
	n     int
}

// overTime groups the weeks into as few periods as keep the bars under
// maxBars, with the periods no one committed in shown empty.
func overTime(weeks []stats.Week) (string, []bar) {
	if len(weeks) == 0 {
		return "", nil
	}
	first, last := weeks[0].Start, weeks[len(weeks)-1].Start
	type period struct {
		title string
		start func(time.Time) time.Time
		next  func(time.Time) time.Time
		label func(time.Time) string
	}
	periods := []period{
		{"Commits per week", func(t time.Time) time.Time { return t }, func(t time.Time) time.Time { return t.AddDate(0, 0, 7) },
			func(t time.Time) string { return "Week of " + t.Format("2006-01-02") }},
		{"Commits per month", func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC) },
			func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }, func(t time.Time) string { return t.Format("January 2006") }},
		{"Commits per quarter", func(t time.Time) time.Time {
			return time.Date(t.Year(), (t.Month()-1)/3*3+1, 1, 0, 0, 0, 0, time.UTC)
		}, func(t time.Time) time.Time { return t.AddDate(0, 3, 0) },
			func(t time.Time) string { return fmt.Sprintf("Q%d %d", (int(t.Month())-1)/3+1, t.Year()) }},
		{"Commits per year", func(t time.Time) time.Time { return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC) },
			func(t time.Time) time.Time { return t.AddDate(1, 0, 0) }, func(t time.Time) string { return strconv.Itoa(t.Year()) }},
	}
	for k, p := range periods {
		n := 0
		for d := p.start(first); !d.After(last); d = p.next(d) {
			n++
		}
		if n > maxBars && k < len(periods)-1 {
			continue
		}
		var bars []bar
		w := 0
		for d := p.start(first); !d.After(last); d = p.next(d) {
			end := p.next(d)
			b := bar{label: p.label(d)}
			for ; w < len(weeks) && weeks[w].Start.Before(end); w++ {
				b.n += weeks[w].Commits
			}
			bars = append(bars, b)
		}
		return p.title, bars
	}
	return "", nil
}

func weekdayBars(st stats.Stats) []bar {
	// Monday first, as the weeks are.
	var out []bar
	for i := range 7 {
		d := (i + 1) % 7
		out = append(out, bar{time.Weekday(d).String(), st.Weekday[d]})
	}
	return out
}

func hourBars(st stats.Stats) []bar {
	var out []bar
	for h := range 24 {
		out = append(out, bar{fmt.Sprintf("%02d:00 to %02d:59", h, h), st.Hour[h]})
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
	ui.Column(c).Gap(6).FillWidth().Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, title).FontSize(12).Bold()
			ui.Text(c, "most "+strconv.Itoa(most)).FontSize(12).TextColor(th.TextMuted)
		})
		ui.Row(c).Gap(2).Height(height).FillWidth().AlignItems(ui.End).Children(func() {
			for i, b := range bars {
				tip := b.label + ": " + commitCount(b.n)
				ui.Column(c).Key(i).Grow(1).FillHeight().Justify(ui.End).Tooltip(tip).Label(tip).Role(ui.RoleImage).Children(func() {
					if b.n > 0 {
						h := max(2, height*float32(b.n)/float32(most))
						ui.Box(c).Height(h).FillWidth().Background(th.Accent).Radius(min(4, h/2), min(4, h/2), 0, 0)
					}
				})
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(th.TextMuted.Alpha(0.3))
		if len(bars) > 1 {
			ui.Row(c).FillWidth().Children(func() {
				ui.Text(c, bars[0].label).FontSize(11).TextColor(th.TextMuted).SingleLine()
				ui.Spacer(c)
				ui.Text(c, bars[len(bars)-1].label).FontSize(11).TextColor(th.TextMuted).SingleLine()
			})
		}
	})
}

func commitCount(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return strconv.Itoa(n) + " commits"
}

func (a *App) authorTable(c *ui.Context, t *tab, st stats.Stats) {
	th := c.Theme()
	ss := &t.stats
	authors := st.Authors
	ss.authors.Label = func(i int) string {
		au := authors[i]
		return fmt.Sprintf("%s, %s", redact.Control(au.Name), commitCount(au.Commits))
	}
	ui.Column(c).Grow(1).MinHeight(160).Children(func() {
		ui.Row(c).Padding(4, 8).Gap(8).Children(func() {
			ui.Text(c, "Person").FontSize(12).Bold().Grow(1)
			ui.Text(c, "Commits").FontSize(12).Bold().Width(72).TextAlign(ui.End)
			ui.Text(c, "Share").FontSize(12).Bold().Width(56).TextAlign(ui.End)
			ui.Text(c, "Active").FontSize(12).Bold().Width(190)
		})
		ui.List(c, &ss.authors, len(authors), func(i int) {
			au := authors[i]
			ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Text(c, redact.Control(au.Name)+" <"+redact.Control(au.Email)+">").SingleLine().Grow(1).Selectable()
				ui.Text(c, strconv.Itoa(au.Commits)).Width(72).TextAlign(ui.End)
				ui.Text(c, fmt.Sprintf("%.1f%%", 100*float64(au.Commits)/float64(st.Commits))).Width(56).TextAlign(ui.End).TextColor(th.TextMuted)
				ui.Text(c, time.Unix(au.First, 0).Format("2006-01-02")+" to "+time.Unix(au.Last, 0).Format("2006-01-02")).
					FontSize(12).Width(190).TextColor(th.TextMuted)
			})
		}).Label("Authors").Grow(1)
	})
}
