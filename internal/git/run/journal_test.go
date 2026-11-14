package run

import (
	"context"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

func TestJournal(t *testing.T) {
	r := initGitDir(t)
	other := initGitDir(t)
	rn := runner(t, 4)
	ctx := context.Background()
	if _, err := rn.Run(ctx, &Cmd{GitDir: r, Read: true, Args: []string{"rev-parse", "--git-dir"}}); err != nil {
		t.Fatal(err)
	}
	// A secret in an argument and git's complaint about it stay out.
	rn.Run(ctx, &Cmd{GitDir: r, Read: true, Args: []string{"rev-parse", "--verify", "https://user:hunter2@example.invalid/x"}})
	rn.Run(ctx, &Cmd{GitDir: other, Read: true, Args: []string{"rev-parse", "HEAD"}})
	recs := rn.Journal().For(r)
	if len(recs) != 2 {
		t.Fatalf("%d records, want 2: %+v", len(recs), recs)
	}
	ok, failed := recs[0], recs[1]
	if !ok.Read || ok.Exit != 0 || ok.Err != "" || !strings.Contains(ok.Line, "rev-parse --git-dir") || !strings.Contains(ok.Stdout, ".") || ok.StdoutLen == 0 {
		t.Fatalf("first %+v", ok)
	}
	if failed.Exit <= 0 || failed.Err == "" || failed.Stderr == "" {
		t.Fatalf("second %+v", failed)
	}
	for _, s := range []string{failed.Line, failed.Err, failed.Stderr} {
		if strings.Contains(s, "hunter2") {
			t.Fatalf("secret kept: %q", s)
		}
	}
	if got := rn.Journal().For(other); len(got) != 1 {
		t.Fatalf("other repository %+v", got)
	}
	// A hardened runner shares the journal.
	h := rn.Hardened(nil)
	v := rn.Journal().Version()
	h.Run(ctx, &Cmd{GitDir: r, Read: true, Args: []string{"rev-parse", "--git-dir"}})
	if rn.Journal().Version() != v+1 || len(rn.Journal().For(r)) != 3 {
		t.Fatal("hardened runner's command not recorded")
	}
}

func TestJournalBounds(t *testing.T) {
	var j Journal
	big := strings.Repeat("x", maxRecordedBytes)
	for i := range MaxRecords * 2 {
		j.add(Record{GitDir: "g", Line: string(rune('a' + i%26)), Stdout: big, Stderr: big})
	}
	recs := j.For("g")
	if j.bytes > MaxJournalBytes || len(recs) == 0 || len(recs) >= MaxRecords {
		t.Fatalf("%d records in %d bytes", len(recs), j.bytes)
	}
	// The newest is last.
	if want := string(rune('a' + (MaxRecords*2-1)%26)); recs[len(recs)-1].Line != want {
		t.Fatalf("newest %q, want %q", recs[len(recs)-1].Line, want)
	}
	var small Journal
	for i := range MaxRecords + 10 {
		small.add(Record{GitDir: "g", Line: string(rune('a' + i%26))})
	}
	if recs := small.For("g"); len(recs) != MaxRecords || recs[len(recs)-1].Line != string(rune('a'+(MaxRecords+9)%26)) || recs[0].Line != string(rune('a'+10%26)) {
		t.Fatalf("%d records, oldest %q", len(recs), recs[0].Line)
	}
}

func initGitDir(t *testing.T) string { return gittest.Init(t).GitDir() }
