package libstats

import (
	"strings"

	"ramus/internal/git/activity"
)

// Filter picks what a summary counts.
type Filter struct {
	// Since and Until bound the commit time, in seconds, as [Since,
	// Until); zero is open.
	Since, Until int64
	// Mine counts only the user's own commits: those by the repositories'
	// user.email addresses and Emails.
	Mine   bool
	Emails []string
}

// Row is one repository in the comparison.
type Row struct {
	Name, Path string
	Totals     activity.Totals
	// Born is the commit time of its first commit, whatever the filter.
	Born int64
	Size int64
	Err  error
	// Loading is a repository not read yet: Read of Total so far.
	Loading     bool
	Read, Total int
}

// Summary is the statistics of a set of repositories under one filter.
type Summary struct {
	Rows []Row
	// Commits, ActiveDays and Contributors are across every repository:
	// a day or a person counted once.
	Commits, ActiveDays, Contributors int
	Heat                              activity.Heat
	Days                              activity.Days
	Calendar                          activity.Calendar
}

// Summarize counts repos under f; today is today's day number, for the
// current streak. It reads every record, so it runs off the main thread.
func Summarize(repos []Repo, f Filter, today int32) Summary {
	emails := activity.NewEmails(f.Emails...)
	if f.Mine {
		for _, r := range repos {
			if r.Email != "" {
				emails[strings.ToLower(strings.TrimSpace(r.Email))] = true
			}
		}
	}
	s := Summary{Days: activity.Days{}}
	people := map[string]bool{}
	for _, r := range repos {
		row := Row{Name: r.Name, Path: r.Path, Size: r.Size, Err: r.Err, Loading: !r.Done, Read: r.Read, Total: r.Total}
		if rec := r.Records; rec != nil {
			af := activity.Filter{Since: f.Since, Until: f.Until}
			if f.Mine {
				af.Mine = rec.Mine(emails)
			}
			row.Totals = rec.Count(af, &s.Heat, s.Days)
			row.Born, _ = rec.Span()
			rec.People(af, people)
			s.Commits += row.Totals.Commits
		}
		s.Rows = append(s.Rows, row)
	}
	s.ActiveDays, s.Contributors = len(s.Days), len(people)
	s.Calendar = activity.Summarize(s.Days, today)
	return s
}
