package activity

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/mailmap"
	"ramus/internal/git/stats"
)

// Counts over the records match what git log says of the same commits.
func TestCountMatchesGit(t *testing.T) {
	r := gittest.Init(t)
	zones := []string{"+0000", "-0700", "+0900", "+0545"}
	for i := range 30 {
		r.Write("f", strconv.Itoa(i))
		who := "me@example.com"
		if i%3 == 0 {
			who = "other@example.com"
		}
		when := 1_710_000_000 + int64(i)*29*3600
		r.Env = []string{"GIT_AUTHOR_NAME=X", "GIT_AUTHOR_EMAIL=" + who,
			fmt.Sprintf("GIT_AUTHOR_DATE=@%d %s", when, zones[i%4]), fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", when+3600)}
		r.Git("add", "-A")
		r.Git("-c", "commit.gpgsign=false", "commit", "-q", "-m", "c")
	}
	r.Env = nil
	var mm mailmap.Map
	mm.Add([]byte("Me <me@example.com>\n"))

	var ids [][]byte
	for _, h := range strings.Fields(r.Git("rev-list", "--all")) {
		id, _ := hex.DecodeString(h)
		ids = append(ids, id)
	}
	var readers []stats.Reader
	for range 2 {
		p, err := catfile.Start(context.Background(), testrun.Runner(t), r.GitDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		readers = append(readers, p)
	}
	rec, err := Read(context.Background(), readers, ids, &mm, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Len() != 30 || rec.Identities() != 2 {
		t.Fatalf("%d commits, %d identities", rec.Len(), rec.Identities())
	}

	since, until := int64(1_710_000_000+5*29*3600), int64(1_710_000_000+25*29*3600)
	f := Filter{Since: since, Until: until, Mine: rec.Mine(NewEmails("ME@example.com"))}
	var heat Heat
	days := Days{}
	tot := rec.Count(f, &heat, days)

	var wantHeat Heat
	wantDays := Days{}
	wantCommits := 0
	for _, l := range strings.Fields(r.Git("log", "--all", "--date=format:%w,%H", "--format=%ae,%ad,%ct")) {
		p := strings.Split(l, ",")
		ct, _ := strconv.ParseInt(p[3], 10, 64)
		if p[0] != "me@example.com" || ct < since || ct >= until {
			continue
		}
		wd, _ := strconv.Atoi(p[1])
		h, _ := strconv.Atoi(p[2])
		wantHeat[wd][h]++
		wantCommits++
	}
	for _, l := range strings.Fields(r.Git("log", "--all", "--date=format:%Y-%m-%d", "--format=%ae,%ad,%ct")) {
		p := strings.Split(l, ",")
		ct, _ := strconv.ParseInt(p[2], 10, 64)
		if p[0] != "me@example.com" || ct < since || ct >= until {
			continue
		}
		d, _ := time.Parse("2006-01-02", p[1])
		wantDays[int32(d.Unix()/86400)]++
	}
	if tot.Commits != wantCommits || heat != wantHeat || !maps.Equal(days, wantDays) {
		t.Fatalf("commits %d (git %d)\nheat %v\ngit  %v\ndays %v\ngit  %v", tot.Commits, wantCommits, heat, wantHeat, days, wantDays)
	}
	if tot.ActiveDays != len(wantDays) || tot.Contributors != 1 {
		t.Fatalf("totals %+v", tot)
	}
	if all := rec.Count(Filter{}, nil, nil); all.Commits != 30 || all.Contributors != 2 {
		t.Fatalf("all %+v", all)
	}

	var buf bytes.Buffer
	if err := rec.Encode(&buf, "key-1"); err != nil {
		t.Fatal(err)
	}
	back, key, err := Decode(&buf)
	if err != nil || key != "key-1" {
		t.Fatal(err, key)
	}
	if back.Count(f, nil, nil) != tot || back.Identities() != rec.Identities() {
		t.Fatal("decoded records count differently")
	}
	data := buf.Bytes()
	for _, bad := range [][]byte{nil, []byte("nope"), []byte(magic + "\xff\xff\xff\xff")} {
		if _, _, err := Decode(bytes.NewReader(append(bad, data...)[:len(bad)])); err == nil {
			t.Errorf("decoded %q", bad)
		}
	}
}

func TestSummarize(t *testing.T) {
	days := Days{100: 1, 101: 2, 102: 1, 110: 5, 111: 1, 130: 2, 131: 1}
	got := Summarize(days, 132)
	want := Calendar{Longest: 3, Current: 2, LongestEnd: 102, Busiest: 110, BusiestCommits: 5,
		Month: time.Date(1970, 4, 1, 0, 0, 0, 0, time.UTC), MonthCommits: 10}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if c := Summarize(Days{}, 5); c != (Calendar{}) {
		t.Fatalf("empty %+v", c)
	}
}
