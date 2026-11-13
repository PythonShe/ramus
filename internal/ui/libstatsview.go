package ui

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/activity"
	"ramus/internal/libstats"
)

// libStatsState is the statistics across the library's repositories.
type libStatsState struct {
	open bool
	// scope is the group counted; "" for every repository.
	scope    string
	from, to string
	mine     bool
	err      string
	loader   *libstats.Loader
	repos    []libstats.Repo
	loading  bool
	loadedAt string // the scope repos were read for
	// sum is what is shown; summing is set while a newer one is counted.
	sum     *libstats.Summary
	summing bool
	sumGen  int
	table   ui.ListState
	sort    ui.SortOrder
}

func (a *App) openLibStats() {
	s := &a.libStats
	s.open = true
	if !s.loading && s.loadedAt != "\x00"+s.scope {
		a.loadLibStats()
	}
}

// libScope returns the repositories the scope counts, and their names.
func (a *App) libScope() (paths, names []string) {
	s := &a.libStats
	all := a.Library.Search("")
	name := map[string]string{}
	for _, r := range all {
		name[r.Path] = r.Name
	}
	if s.scope == "" {
		for _, r := range all {
			paths, names = append(paths, r.Path), append(names, r.Name)
		}
		return paths, names
	}
	for _, g := range a.Library.Groups() {
		if g.Name != s.scope {
			continue
		}
		for _, p := range g.Paths {
			n := name[p]
			if n == "" {
				n = filepath.Base(p)
			}
			paths, names = append(paths, p), append(names, n)
		}
	}
	return paths, names
}

// loadLibStats reads the scope's repositories, one at a time.
func (a *App) loadLibStats() {
	s := &a.libStats
	if a.Runner == nil || a.Library == nil {
		return
	}
	if s.loader == nil {
		var trusted func(string) bool
		if ts := a.Trust; ts != nil {
			trusted = ts.Trusted
		}
		dir := ""
		if a.CacheDir != "" {
			dir = filepath.Join(a.CacheDir, "activity")
		}
		s.loader = libstats.New(a.Runner, trusted, dir, a.Update)
	}
	paths, names := a.libScope()
	s.repos = make([]libstats.Repo, len(paths))
	for i := range paths {
		s.repos[i] = libstats.Repo{Path: paths[i], Name: names[i]}
	}
	s.loading, s.loadedAt = len(paths) > 0, "\x00"+s.scope
	a.summarize()
	s.loader.Load(paths, names, func(i int, r libstats.Repo) {
		s.repos[i] = r
		if r.Done {
			s.loading = slices.ContainsFunc(s.repos, func(r libstats.Repo) bool { return !r.Done })
			a.summarize()
		} else if s.sum != nil && i < len(s.sum.Rows) {
			row := &s.sum.Rows[i]
			row.Read, row.Total = r.Read, r.Total
		}
	})
}

// summarize counts what is read under the controls, off the main thread.
func (a *App) summarize() {
	s := &a.libStats
	f := libstats.Filter{Mine: s.mine, Emails: a.Settings.Emails}
	since, err := parseDay(s.from)
	var until time.Time
	if err == nil {
		until, err = parseDay(s.to)
	}
	if err != nil {
		s.err = errText(err)
		return
	}
	s.err = ""
	if !since.IsZero() {
		f.Since = since.Unix()
	}
	if !until.IsZero() {
		f.Until = until.AddDate(0, 0, 1).Unix()
	}
	s.sumGen++
	gen, repos, today, update := s.sumGen, slices.Clone(s.repos), activity.DayOf(a.Now()), a.Update
	s.summing = true
	go func() {
		sum := libstats.Summarize(repos, f, today)
		update(func() {
			if s.sumGen == gen {
				s.sum, s.summing = &sum, false
			}
		})
	}()
}

func (a *App) libStatsView(c *ui.Context) {
	s := &a.libStats
	if !s.open {
		if s.loading && s.loader != nil {
			// Closed before every repository was read: stop, and read
			// again when opened.
			s.loader.Cancel()
			s.loading, s.loadedAt = false, ""
		}
		return
	}
	th := c.Theme()
	ui.Modal(c, &s.open, func() {
		ui.Column(c).Width(980).Height(740).Gap(14).Padding(20).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Library Statistics").Bold()
				a.libScopeMenu(c)
				from := ui.TextInput(c, &s.from).Placeholder("From YYYY-MM-DD").Label("From").Width(150)
				to := ui.TextInput(c, &s.to).Placeholder("To YYYY-MM-DD").Label("To").Width(150)
				if from.Submitted() || to.Submitted() {
					a.summarize()
				}
				if ui.Checkbox(c, &s.mine, "Only My Commits").Changed() {
					a.summarize()
				}
				if s.err != "" {
					ui.Text(c, s.err).FontSize(12).TextColor(th.Danger).SingleLine()
				}
				ui.Spacer(c)
				if done := countDone(s.repos); s.loading {
					ui.Text(c, fmt.Sprintf("Read %d of %d repositories", done, len(s.repos))).FontSize(12).TextColor(th.TextMuted)
				}
			})
			sum := s.sum
			switch {
			case len(s.repos) == 0:
				ui.Column(c).Fill().Center().Children(func() {
					ui.Text(c, "No repositories").TextColor(th.TextMuted)
				})
				return
			case sum == nil:
				ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
				return
			}
			ui.Row(c).Gap(24).AlignItems(ui.End).Children(func() {
				ui.Text(c, strconv.Itoa(sum.Commits)).FontSize(28).Bold()
				ui.Text(c, fmt.Sprintf("commits by %s on %s in %s", people(sum.Contributors),
					plural(sum.ActiveDays, "active day"), plural(len(sum.Rows), "repository"))).TextColor(th.TextMuted)
			})
			ui.Row(c).Gap(40).FillWidth().Children(func() {
				ui.Column(c).Gap(6).Children(func() {
					ui.Text(c, "By weekday and hour of the author's day").FontSize(12).Bold()
					heatMap(c, &sum.Heat)
				})
				calendarStats(c, sum.Calendar)
			})
			ui.Column(c).Gap(6).FillWidth().Children(func() {
				ui.Text(c, "Days with commits").FontSize(12).Bold()
				a.calendarView(c, sum)
			})
			a.compareTable(c, sum)
		})
	})
}

func countDone(repos []libstats.Repo) int {
	n := 0
	for _, r := range repos {
		if r.Done {
			n++
		}
	}
	return n
}

func (a *App) libScopeMenu(c *ui.Context) {
	s := &a.libStats
	label := "All Repositories"
	if s.scope != "" {
		label = s.scope
	}
	ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
		if m.Item("All Repositories").Chosen() && s.scope != "" {
			s.scope = ""
			a.loadLibStats()
		}
		for _, g := range a.Library.Groups() {
			if m.Item(g.Name).Chosen() && s.scope != g.Name {
				s.scope = g.Name
				a.loadLibStats()
			}
		}
	})
}

// shade is the step of a sequential scale a count takes, 0 for none and
// 1 to 4 by its share of the most.
func shade(n, most int) int {
	if n <= 0 || most <= 0 {
		return 0
	}
	return min(4, (4*n+most-1)/most)
}

// shadeColor is one hue, light to dark with the count.
func shadeColor(c *ui.Context, step int) ui.Color {
	th := c.Theme()
	if step == 0 {
		return th.TextMuted.Alpha(0.1)
	}
	return th.Accent.Alpha([]float32{0, 0.3, 0.5, 0.75, 1}[step])
}

// heatMap draws commits by weekday, Monday first, and hour.
func heatMap(c *ui.Context, heat *activity.Heat) {
	th := c.Theme()
	most := 0
	for _, row := range heat {
		for _, n := range row {
			most = max(most, n)
		}
	}
	const cell = 14
	ui.Column(c).Gap(2).Children(func() {
		for i := range 7 {
			d := (i + 1) % 7
			ui.Row(c).Key(d).Gap(2).AlignItems(ui.Center).Children(func() {
				ui.Text(c, time.Weekday(d).String()[:3]).FontSize(11).TextColor(th.TextMuted).Width(30)
				for h := range 24 {
					n := heat[d][h]
					tip := fmt.Sprintf("%s %02d:00 to %02d:59: %s", time.Weekday(d), h, h, commitCount(n))
					ui.Box(c).Key(h).Size(cell, cell).Radius(2).Background(shadeColor(c, shade(n, most))).
						Tooltip(tip).Label(tip).Role(ui.RoleImage)
				}
			})
		}
		ui.Row(c).Gap(2).Children(func() {
			ui.Box(c).Width(30)
			for h := 0; h < 24; h += 6 {
				ui.Text(c, fmt.Sprintf("%02d", h)).FontSize(11).TextColor(th.TextMuted).Width(6*cell + 5*2 + 2)
			}
		})
	})
}

// calendarWeeks is how far back the calendar goes.
const calendarWeeks = 53

// calendarView shows a year of days, a week a column, ending with the
// range, and the streaks and busiest times beside it.
func (a *App) calendarView(c *ui.Context, sum *libstats.Summary) {
	th := c.Theme()
	s := &a.libStats
	end := activity.DayOf(a.Now())
	if until, err := parseDay(s.to); err == nil && !until.IsZero() {
		end = activity.DayOf(until)
	}
	// Columns start on Monday; day 0 (1970-01-01) was a Thursday.
	wd := (int(end)%7 + 7 + 3) % 7 // Monday 0
	first := end - int32(wd) - 7*(calendarWeeks-1)
	most := 0
	for d := first; d <= end; d++ {
		most = max(most, sum.Days[d])
	}
	const cell = 13
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).Gap(2).Children(func() {
			for w := range calendarWeeks {
				ui.Column(c).Key(w).Gap(2).Children(func() {
					for i := range 7 {
						d := first + int32(7*w+i)
						if d > end {
							break
						}
						n := sum.Days[d]
						tip := activity.Date(d).Format("Mon 2006-01-02") + ": " + commitCount(n)
						ui.Box(c).Key(i).Size(cell, cell).Radius(2).Background(shadeColor(c, shade(n, most))).
							Tooltip(tip).Label(tip).Role(ui.RoleImage)
					}
				})
			}
		})
		ui.Row(c).FillWidth().Children(func() {
			ui.Text(c, activity.Date(first).Format("2006-01-02")).FontSize(11).TextColor(th.TextMuted)
			ui.Spacer(c)
			ui.Text(c, activity.Date(end).Format("2006-01-02")).FontSize(11).TextColor(th.TextMuted)
		})
	})
}

// calendarStats says the streaks and the busiest day and month.
func calendarStats(c *ui.Context, cal activity.Calendar) {
	th := c.Theme()
	stat := func(title, value, detail string) {
		ui.Column(c).Gap(2).Width(150).Children(func() {
			ui.Text(c, title).FontSize(11).TextColor(th.TextMuted)
			ui.Text(c, value).Bold()
			if detail != "" {
				ui.Text(c, detail).FontSize(11).TextColor(th.TextMuted)
			}
		})
	}
	day := func(d int32) string { return activity.Date(d).Format("2006-01-02") }
	ui.Column(c).Gap(16).Padding(20, 0, 0, 0).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
			if cal.Longest > 0 {
				stat("Longest streak", plural(cal.Longest, "day"), "to "+day(cal.LongestEnd))
			} else {
				stat("Longest streak", "None", "")
			}
			stat("Current streak", plural(cal.Current, "day"), "")
		})
		if cal.BusiestCommits > 0 {
			ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
				stat("Busiest day", day(cal.Busiest), commitCount(cal.BusiestCommits))
				stat("Most active month", cal.Month.Format("January 2006"), commitCount(cal.MonthCommits))
			})
		}
	})
}

// compareTable is the repositories side by side.
func (a *App) compareTable(c *ui.Context, sum *libstats.Summary) {
	th := c.Theme()
	s := &a.libStats
	rows := slices.Clone(sum.Rows)
	if s.sort.Column == "" {
		s.sort = ui.SortOrder{Column: "Commits", Descending: true}
	}
	key := func(r libstats.Row) int64 {
		switch s.sort.Column {
		case "Active Days":
			return int64(r.Totals.ActiveDays)
		case "People":
			return int64(r.Totals.Contributors)
		case "Age":
			return -r.Born
		case "Size":
			return r.Size
		}
		return int64(r.Totals.Commits)
	}
	slices.SortStableFunc(rows, func(x, y libstats.Row) int {
		var o int
		if s.sort.Column == "Repository" {
			o = cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
		} else {
			o = cmp.Compare(key(x), key(y))
		}
		if s.sort.Descending {
			o = -o
		}
		return o
	})
	cols := []ui.TableColumn{
		{Title: "Repository", Sortable: true},
		{Title: "Commits", Width: 100, Align: ui.End, Sortable: true},
		{Title: "Active Days", Width: 100, Align: ui.End, Sortable: true},
		{Title: "People", Width: 80, Align: ui.End, Sortable: true},
		{Title: "Age", Width: 100, Align: ui.End, Sortable: true},
		{Title: "Size", Width: 90, Align: ui.End, Sortable: true},
	}
	s.table.Sort = &s.sort
	s.table.Label = func(i int) string {
		r := rows[i]
		return fmt.Sprintf("%s, %s", r.Name, commitCount(r.Totals.Commits))
	}
	now := a.Now().Unix()
	ui.Table(c, &s.table, cols, len(rows), func(i, col int) {
		r := rows[i]
		pending := r.Loading || r.Err != nil
		switch col {
		case 0:
			ui.Column(c).Children(func() {
				ui.Text(c, r.Name).SingleLine()
				switch {
				case r.Err != nil:
					ui.Text(c, errText(r.Err)).FontSize(11).TextColor(th.Danger).SingleLine().Tooltip(errText(r.Err))
				case r.Loading && r.Total > 0:
					ui.Text(c, fmt.Sprintf("Reading %d of %d commits", r.Read, r.Total)).FontSize(11).TextColor(th.TextMuted).SingleLine()
				}
			})
		case 1:
			if !pending {
				ui.Text(c, strconv.Itoa(r.Totals.Commits))
			} else if r.Loading {
				ui.Spinner(c).Size(12, 12)
			}
		case 2:
			if !pending {
				ui.Text(c, strconv.Itoa(r.Totals.ActiveDays))
			}
		case 3:
			if !pending {
				ui.Text(c, strconv.Itoa(r.Totals.Contributors))
			}
		case 4:
			if !pending && r.Born > 0 {
				ui.Text(c, age(now-r.Born))
			}
		case 5:
			if !pending {
				ui.Text(c, byteSize(r.Size))
			}
		}
	}).Grow(1).MinHeight(180)
}

// age says a span of seconds in the largest unit that fits.
func age(secs int64) string {
	days := secs / 86400
	switch {
	case days >= 365:
		return plural(int(days/365), "year")
	case days >= 30:
		return plural(int(days/30), "month")
	}
	return plural(int(max(days, 0)), "day")
}
