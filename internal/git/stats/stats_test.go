package stats

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/mailmap"
)

// The counts match what git log and shortlog say of the same commits,
// mailmap applied, in each author's own time zone.
func TestCollectMatchesGit(t *testing.T) {
	r := gittest.Init(t)
	people := []string{"Ada <ada@example.com>", "ada l <ADA@example.com>", "Bob <bob@example.com>", "Cy <cy@old.example.com>"}
	zones := []string{"+0000", "-0800", "+0530", "+1400", "-1100"}
	for i := range 40 {
		r.Write("f", strconv.Itoa(i))
		// Spread over weeks, hours and zones, across midnight and the
		// turn of a year.
		when := 1_703_900_000 + int64(i)*37*3600
		r.Env = []string{
			"GIT_AUTHOR_NAME=" + strings.Fields(people[i%4])[0],
			"GIT_AUTHOR_EMAIL=" + strings.Trim(strings.Fields(people[i%4])[len(strings.Fields(people[i%4]))-1], "<>"),
			fmt.Sprintf("GIT_AUTHOR_DATE=@%d %s", when, zones[i%5]),
			fmt.Sprintf("GIT_COMMITTER_DATE=@%d %s", when+int64(i%3)*86400, zones[(i+2)%5]),
		}
		r.Git("add", "-A")
		r.Git("-c", "commit.gpgsign=false", "commit", "-q", "-m", "c"+strconv.Itoa(i))
	}
	r.Env = nil
	mmText := "Ada Lovelace <ada@example.com>\nCy <cy@example.com> <cy@old.example.com>\n"
	r.Write(".mailmap", mmText)
	var mm mailmap.Map
	mm.Add([]byte(mmText))

	var ids [][]byte
	for _, h := range strings.Fields(r.Git("rev-list", "--all")) {
		id, _ := hex.DecodeString(h)
		ids = append(ids, id)
	}
	var readers []Reader
	for range 3 {
		p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		readers = append(readers, p)
	}
	s, err := Collect(context.Background(), readers, ids, &mm, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Commits != 40 || len(s.Weeks) < 8 || len(s.Authors) != 4 {
		t.Fatalf("%d commits, %d weeks, %d authors", s.Commits, len(s.Weeks), len(s.Authors))
	}

	var got []string
	for _, a := range s.Authors {
		got = append(got, fmt.Sprintf("%d\t%s <%s>", a.Commits, a.Name, a.Email))
	}
	var want []string
	for _, l := range strings.Split(strings.TrimSpace(r.Git("shortlog", "-sne", "--all")), "\n") {
		want = append(want, strings.TrimSpace(l))
	}
	if !slices.Equal(got, want) {
		t.Errorf("authors\n%q\ngit\n%q", got, want)
	}

	weekday, hour, weeks := map[int]int{}, map[int]int{}, map[string]int{}
	for _, l := range strings.Fields(r.Git("log", "--all", "--date=format:%w,%H,%G-%V", "--format=%ad,%cd")) {
		f := strings.Split(l, ",")
		w, _ := strconv.Atoi(f[0])
		h, _ := strconv.Atoi(f[1])
		weekday[w]++
		hour[h]++
		weeks[f[5]]++
	}
	for d := range 7 {
		if s.Weekday[d] != weekday[d] {
			t.Errorf("weekday %d: %d, git %d", d, s.Weekday[d], weekday[d])
		}
	}
	for h := range 24 {
		if s.Hour[h] != hour[h] {
			t.Errorf("hour %d: %d, git %d", h, s.Hour[h], hour[h])
		}
	}
	gotWeeks := map[string]int{}
	for _, w := range s.Weeks {
		if w.Start.Weekday() != 1 {
			t.Errorf("week starts on %v", w.Start.Weekday())
		}
		y, n := w.Start.ISOWeek()
		gotWeeks[fmt.Sprintf("%d-%02d", y, n)] = w.Commits
	}
	if !maps.Equal(gotWeeks, weeks) {
		t.Errorf("weeks %v, git %v", gotWeeks, weeks)
	}
}
