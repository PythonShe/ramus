package forge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const listAnswer = `{"data":{"repository":{"pullRequests":{"totalCount":3,"nodes":[
{"number":7,"title":"Add a thing","url":"https://github.com/o/r/pull/7","isDraft":false,"state":"OPEN","author":{"login":"ann"},
 "headRefName":"thing","baseRefName":"main","updatedAt":"2026-11-13T16:04:48Z","reviewDecision":"CHANGES_REQUESTED",
 "headRepositoryOwner":{"login":"ann"},"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}},
{"number":5,"title":"Draft","url":"https://evil.example/x","isDraft":true,"state":"OPEN","author":null,
 "headRefName":"d","baseRefName":"main","updatedAt":"2026-11-12T10:00:00Z","reviewDecision":null,
 "headRepositoryOwner":{"login":"o"},"commits":{"nodes":[]}}]}}}}`

func TestParseOpenPulls(t *testing.T) {
	list, more, err := parseOpenPulls([]byte(listAnswer))
	if err != nil || !more || len(list) != 2 {
		t.Fatalf("%+v %v %v", list, more, err)
	}
	a, b := list[0], list[1]
	if a.Number != 7 || a.Author != "ann" || a.Base != "main" || a.Decision != ReviewChangesRequested || a.Checks != ChecksPassing || a.Updated.IsZero() {
		t.Errorf("first %+v", a)
	}
	if !b.Draft || b.Author != "ghost" || b.Decision != ReviewNone {
		t.Errorf("second %+v", b)
	}
	if _, _, err := parseOpenPulls([]byte(`{"errors":[{"message":"Could not resolve to a Repository"}]}`)); err == nil {
		t.Error("an error answered is taken")
	}
}

const reviewAnswer = `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{
"number":7,"title":"Add a thing","url":"https://github.com/o/r/pull/7","body":"Why\n","state":"OPEN","isDraft":false,
"author":{"login":"ann"},"baseRefName":"main","headRefName":"thing",
"baseRefOid":"1111111111111111111111111111111111111111","headRefOid":"2222222222222222222222222222222222222222",
"reviewDecision":"REVIEW_REQUIRED","createdAt":"2026-11-10T09:00:00Z",
"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING"}}}]},
"comments":{"totalCount":1,"nodes":[{"id":"IC_1","databaseId":11,"author":{"login":"bob"},"body":"Looks fine","createdAt":"2026-11-11T09:00:00Z"}]},
"reviews":{"totalCount":2,"nodes":[{"author":{"login":"bob"},"state":"COMMENTED","body":"","submittedAt":"2026-11-11T10:00:00Z"}]},
"reviewThreads":{"totalCount":2,"nodes":[
 {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.go","line":12,"startLine":10,"originalLine":12,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C1","databaseId":42,"author":{"login":"bob"},"body":"Why here?","createdAt":"2026-11-11T10:00:00Z"}]}},
 {"id":"T2","isResolved":true,"isOutdated":true,"path":"b.go","line":null,"startLine":null,"originalLine":3,"diffSide":"LEFT",
  "viewerCanResolve":false,"viewerCanUnresolve":true,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C2","databaseId":43,"author":{"login":"me"},"body":"Gone","createdAt":"2026-11-11T11:00:00Z"}]}}]}}}}}`

func TestParseReview(t *testing.T) {
	rv, err := parseReview([]byte(reviewAnswer))
	if err != nil {
		t.Fatal(err)
	}
	if rv.Number != 7 || rv.Viewer != "me" || rv.Author != "ann" || rv.Base != "main" || rv.HeadOID[0] != '2' ||
		rv.Decision != ReviewRequired || rv.Checks != ChecksPending || rv.Body != "Why\n" {
		t.Errorf("pull %+v", rv)
	}
	if len(rv.Comments) != 1 || rv.Comments[0].Author != "bob" || len(rv.Reviews) != 1 || rv.Reviews[0].State != ReviewCommented {
		t.Errorf("conversation %+v %+v", rv.Comments, rv.Reviews)
	}
	// Another review is on the host only.
	if !rv.More {
		t.Error("the review past those read is not told")
	}
	if len(rv.Threads) != 2 {
		t.Fatalf("threads %+v", rv.Threads)
	}
	a, b := rv.Threads[0], rv.Threads[1]
	if a.Path != "a.go" || a.Line != 12 || a.StartLine != 10 || a.Side != Right || !a.CanResolve || a.Comments[0].DatabaseID != 42 {
		t.Errorf("first thread %+v", a)
	}
	if !b.Outdated || !b.Resolved || b.Line != 0 || b.OriginalLine != 3 || b.Side != Left {
		t.Errorf("outdated thread %+v", b)
	}
	if _, err := parseReview([]byte(`{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":null}}}`)); !errors.Is(err, ErrNotFound) {
		t.Errorf("no pull request: %v", err)
	}
}

// recordingTool is a gh that keeps its arguments, one a line, and its
// standard input, and answers out.
func recordingTool(t *testing.T, out string) (env []string, args, stdin func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + dir + "/args'\ncat > '" + dir + "/stdin'\ncat <<'EOF'\n" + out + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	read := func(name string) func() string {
		return func() string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
	}
	return []string{"PATH=" + dir + ":/usr/bin:/bin"}, read("args"), read("stdin")
}

func TestSubmitReview(t *testing.T) {
	env, args, stdin := recordingTool(t, `{}`)
	c, err := Find(env, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	r := Repo{GitHub, "github.com", "o/r"}
	head := strings.Repeat("ab", 20)
	drafts := []Draft{{Path: "a.go", Side: Right, Line: 12, Body: "one line"}, {Path: "b.go", Side: Left, Line: 5, StartLine: 3, Body: "lines"}}
	if err := c.SubmitReview(context.Background(), r, 7, Revs{Head: head}, VerdictRequestChanges, "Please", drafts); err != nil {
		t.Fatal(err)
	}
	if got := args(); !strings.Contains(got, "POST\nrepos/o/r/pulls/7/reviews\n--input\n-\n") {
		t.Errorf("args %q", got)
	}
	var sent struct {
		CommitID string `json:"commit_id"`
		Event    string `json:"event"`
		Body     string `json:"body"`
		Comments []map[string]any
	}
	if err := json.Unmarshal([]byte(stdin()), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.CommitID != head || sent.Event != "REQUEST_CHANGES" || sent.Body != "Please" || len(sent.Comments) != 2 {
		t.Fatalf("sent %+v", sent)
	}
	if c0 := sent.Comments[0]; c0["side"] != "RIGHT" || c0["line"] != 12.0 || c0["start_line"] != nil {
		t.Errorf("one line %v", c0)
	}
	if c1 := sent.Comments[1]; c1["side"] != "LEFT" || c1["start_line"] != 3.0 || c1["start_side"] != "LEFT" {
		t.Errorf("several lines %v", c1)
	}
	if err := c.SubmitReview(context.Background(), r, 7, Revs{Head: "HEAD"}, VerdictComment, "", nil); err == nil {
		t.Error("a revision taken for a commit id")
	}

	if err := c.Reply(context.Background(), r, 7, Thread{Comments: []Comment{{DatabaseID: 42}}}, "Because"); err != nil {
		t.Fatal(err)
	}
	if got := args(); !strings.Contains(got, "repos/o/r/pulls/7/comments/42/replies\n") || stdin() != `{"body":"Because"}` {
		t.Errorf("reply %q %q", got, stdin())
	}
	if err := c.AddComment(context.Background(), r, 7, "Thanks"); err != nil {
		t.Fatal(err)
	}
	if got := args(); !strings.Contains(got, "repos/o/r/issues/7/comments\n") {
		t.Errorf("comment %q", got)
	}
	if err := c.Resolve(context.Background(), r, 7, Thread{ID: "T1"}, true); err != nil {
		t.Fatal(err)
	}
	if got := args(); !strings.Contains(got, "resolveReviewThread") || !strings.Contains(got, "id=T1\n") {
		t.Errorf("resolve %q", got)
	}

	gl := Repo{GitLab, "gitlab.com", "g/p"}
	if _, _, err := c.OpenPulls(context.Background(), gl); !errors.Is(err, ErrNoCLI) {
		t.Errorf("GitLab through gh: %v", err)
	}
}
