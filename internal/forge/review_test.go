package forge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const listAnswer = `{"data":{"repository":{"pullRequests":{"totalCount":3,"nodes":[
{"id":"PR_7","number":7,"title":"Add a thing","url":"https://github.com/o/r/pull/7","isDraft":false,"state":"OPEN","author":{"login":"ann"},
 "headRefName":"thing","baseRefName":"main","updatedAt":"2026-11-13T16:04:48Z","headRepositoryOwner":{"login":"ann"}},
{"id":"PR_5","number":5,"title":"Draft","url":"https://evil.example/x","isDraft":true,"state":"OPEN","author":null,
 "headRefName":"d","baseRefName":"main","updatedAt":"2026-11-12T10:00:00Z","headRepositoryOwner":{"login":"o"}}]}}}}`

func TestParseOpenPulls(t *testing.T) {
	list, more, err := parseOpenPulls([]byte(listAnswer))
	if err != nil || !more || len(list) != 2 {
		t.Fatalf("%+v %v %v", list, more, err)
	}
	a, b := list[0], list[1]
	if a.Number != 7 || a.ID != "PR_7" || a.Author != "ann" || a.Base != "main" || a.Updated.IsZero() || a.Detailed {
		t.Errorf("first %+v", a)
	}
	if !b.Draft || b.Author != "ghost" || b.ID != "PR_5" {
		t.Errorf("second %+v", b)
	}
	if _, _, err := parseOpenPulls([]byte(`{"errors":[{"message":"Could not resolve to a Repository"}]}`)); err == nil {
		t.Error("an error answered is taken")
	}
}

func TestParseDetails(t *testing.T) {
	// One gone since it was listed: null, and named among the errors.
	got, err := parseDetails([]byte(`{"data":{"nodes":[
{"id":"PR_7","reviewDecision":"CHANGES_REQUESTED","additions":120,"deletions":7,"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}},
null,
{"id":"PR_5","reviewDecision":null,"additions":0,"deletions":2,"commits":{"nodes":[]}}]},
"errors":[{"message":"Could not resolve to a node with the global id of 'PR_6'"}]}`))
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if d := got["PR_7"]; d.decision != ReviewChangesRequested || d.checks != ChecksPassing || d.added != 120 || d.deleted != 7 {
		t.Errorf("first %+v", d)
	}
	if d := got["PR_5"]; d.decision != ReviewNone || d.checks != NoChecks || d.deleted != 2 {
		t.Errorf("second %+v", d)
	}
	if _, err := parseDetails([]byte(`{"data":{"nodes":[null]},"errors":[{"message":"Something went wrong"}]}`)); err == nil {
		t.Error("an error with no answer is taken")
	}
}

// detailsTool is a gh that answers the details of the pull requests a
// query names, +N lines for the one whose id is PR_N, and logs each
// query's ids on a line. When its folder holds a file named fail, it
// fails the first query as GitHub does one it cut off; the pull request
// named in a file gone there it no longer knows, which to gh is a
// failure with the others' answers printed.
func detailsTool(t *testing.T) (dir string, env []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir = t.TempDir()
	script := `#!/bin/sh
d='` + dir + `'
for a; do q=$a; done
if [ -f "$d/fail" ] && mkdir "$d/failed" 2>/dev/null; then echo failed >> "$d/asked"; echo '<html>502 Bad Gateway</html>'; echo 'gh: HTTP 502' >&2; exit 1; fi
ids=$(printf '%s' "$q" | tr -c 'PR_0-9' '\n' | grep '^PR_')
echo $ids >> "$d/asked"
gone=$(cat "$d/gone" 2>/dev/null)
missed=
printf '{"data":{"nodes":['
sep=
for id in $ids; do
  if [ "$id" = "$gone" ]; then printf '%snull' "$sep"; missed=1
  else printf '%s{"id":"%s","reviewDecision":"APPROVED","additions":%s,"deletions":1,"commits":{"nodes":[]}}' "$sep" "$id" "${id#PR_}"; fi
  sep=,
done
printf ']}'
if [ -n "$missed" ]; then
  printf ',"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a node with the global id of %s"}]}\n' "$gone"
  echo "gh: Could not resolve to a node with the global id of '$gone'" >&2
  exit 1
fi
printf '}\n'
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, []string{"PATH=" + dir + ":/usr/bin:/bin"}
}

// The details are asked for a few pull requests a query, those already
// read left alone, and a query the host cut off is asked again.
func TestPullDetails(t *testing.T) {
	dir, env := detailsTool(t)
	c, err := Find(env, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	r := Repo{GitHub, "github.com", "o/r"}
	list := make([]Listed, 2*detailBatch+5)
	for i := range list {
		list[i] = Listed{Pull: Pull{Number: i + 1}, ID: "PR_" + strconv.Itoa(i+1)}
	}
	list[0].Detailed, list[0].Added = true, 999
	if err := os.WriteFile(filepath.Join(dir, "fail"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := c.PullDetails(context.Background(), r, list, func() { calls++ }); err != nil {
		t.Fatal(err)
	}
	for i, l := range list {
		want := i + 1
		if i == 0 {
			want = 999
		}
		if !l.Detailed || l.Added != want || i > 0 && (l.Decision != ReviewApproved || l.Deleted != 1) {
			t.Fatalf("pull request %d: %+v", i+1, l)
		}
	}
	asked, _ := os.ReadFile(filepath.Join(dir, "asked"))
	var sizes []int
	failed := 0
	for _, line := range strings.Split(strings.TrimSpace(string(asked)), "\n") {
		if line == "failed" {
			failed++
			continue
		}
		sizes = append(sizes, len(strings.Fields(line)))
	}
	slices.Sort(sizes)
	if failed != 1 || !slices.Equal(sizes, []int{4, detailBatch, detailBatch}) || calls != 3 {
		t.Fatalf("asked %q: %d queries told of", asked, calls)
	}

	// A host that fails twice is not asked a third time, and what was
	// read stays.
	for i := range list {
		list[i].Detailed = i < detailBatch
	}
	failing := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho 'gh: HTTP 502' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err = Find([]string{"PATH=" + filepath.Dir(failing) + ":/usr/bin:/bin"}, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	var se *ServerError
	if err := c.PullDetails(context.Background(), r, list, nil); !errors.As(err, &se) || se.Status != 502 {
		t.Fatalf("a host failing: %v", err)
	}
	if !list[0].Detailed || list[len(list)-1].Detailed {
		t.Fatal("what was read is kept, and no more")
	}
}

// A pull request gone since it was listed costs the others of its query
// nothing, nor the queries after it.
func TestPullDetailsOneGone(t *testing.T) {
	dir, env := detailsTool(t)
	c, err := Find(env, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone"), []byte("PR_3"), 0o644); err != nil {
		t.Fatal(err)
	}
	list := make([]Listed, detailBatch+5)
	for i := range list {
		list[i] = Listed{Pull: Pull{Number: i + 1}, ID: "PR_" + strconv.Itoa(i+1)}
	}
	if err := c.PullDetails(context.Background(), Repo{GitHub, "github.com", "o/r"}, list, nil); err != nil {
		t.Fatal(err)
	}
	for i, l := range list {
		if l.Detailed != (i != 2) || l.Detailed && l.Added != i+1 {
			t.Fatalf("pull request %d: %+v", i+1, l)
		}
	}

	// When it is the only one asked of, nothing was answered: the error.
	alone := []Listed{{Pull: Pull{Number: 3}, ID: "PR_3"}}
	if err := c.PullDetails(context.Background(), Repo{GitHub, "github.com", "o/r"}, alone, nil); err == nil || !strings.Contains(err.Error(), "Could not resolve") || alone[0].Detailed {
		t.Fatalf("the only one gone: %v, %+v", err, alone[0])
	}
}

const reviewAnswer = `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{
"number":7,"title":"Add a thing","url":"https://github.com/o/r/pull/7","body":"Why\n","state":"OPEN","isDraft":false,
"author":{"login":"ann"},"baseRefName":"main","headRefName":"thing",
"baseRefOid":"1111111111111111111111111111111111111111","headRefOid":"2222222222222222222222222222222222222222",
"reviewDecision":"REVIEW_REQUIRED","createdAt":"2026-11-10T09:00:00Z","additions":3,"deletions":1,
"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING"}}}]},
"comments":{"totalCount":1,"nodes":[{"id":"IC_1","databaseId":11,"author":{"login":"bob"},"body":"Looks fine","createdAt":"2026-11-11T09:00:00Z"}]},
"reviews":{"totalCount":2,"nodes":[{"author":{"login":"bob"},"state":"COMMENTED","body":"","submittedAt":"2026-11-11T10:00:00Z"}]},
"reviewThreads":{"totalCount":3,"nodes":[
 {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.go","line":12,"startLine":10,"originalLine":12,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C1","databaseId":42,"author":{"login":"bob"},"body":"Why here?","createdAt":"2026-11-11T10:00:00Z"}]}},
 {"id":"T2","isResolved":true,"isOutdated":true,"path":"b.go","line":null,"startLine":null,"originalLine":3,"diffSide":"LEFT",
  "viewerCanResolve":false,"viewerCanUnresolve":true,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C2","databaseId":43,"author":{"login":"me"},"body":"Gone","createdAt":"2026-11-11T11:00:00Z"}]}},
 {"id":"T3","isResolved":false,"isOutdated":false,"path":"c.go","line":null,"startLine":null,"originalLine":null,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C3","databaseId":44,"author":{"login":"bob"},"body":"Split this file","createdAt":"2026-11-11T12:00:00Z"}]}}]}}}}}`

// A review whose answer passes the bound on other answers still opens:
// bots' walkthroughs after every push make one that long.
func TestLongReviewOpens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	dir := t.TempDir()
	answer := strings.Replace(reviewAnswer, `"body":"Why\n"`, `"body":"`+strings.Repeat("walkthrough ", 800_000)+`"`, 1)
	if len(answer) <= maxOutput {
		t.Fatalf("answer of %d bytes", len(answer))
	}
	if err := os.WriteFile(filepath.Join(dir, "answer.json"), []byte(answer), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat '" + filepath.Join(dir, "answer.json") + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := Find([]string{"PATH=" + dir + ":/usr/bin:/bin"}, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	rv, err := c.PullReview(context.Background(), Repo{GitHub, "github.com", "o/r"}, 7)
	if err != nil || rv.Number != 7 || len(rv.Body) < maxOutput {
		t.Fatalf("%d bytes of body, %v", len(rv.Body), err)
	}
}

func TestParseReview(t *testing.T) {
	rv, err := parseReview([]byte(reviewAnswer))
	if err != nil {
		t.Fatal(err)
	}
	if rv.Added != 3 || rv.Deleted != 1 {
		t.Errorf("lines +%d -%d", rv.Added, rv.Deleted)
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
	if len(rv.Threads) != 3 {
		t.Fatalf("threads %+v", rv.Threads)
	}
	a, b, f := rv.Threads[0], rv.Threads[1], rv.Threads[2]
	if a.Path != "a.go" || a.Line != 12 || a.StartLine != 10 || a.Side != Right || !a.CanResolve || a.Comments[0].DatabaseID != 42 || a.File {
		t.Errorf("first thread %+v", a)
	}
	if !b.Outdated || !b.Resolved || b.Line != 0 || b.OriginalLine != 3 || b.Side != Left || b.File {
		t.Errorf("outdated thread %+v", b)
	}
	// One on the whole file has no line, and is not outdated.
	if !f.File || f.Outdated || f.Line != 0 || f.OriginalLine != 0 || f.Path != "c.go" {
		t.Errorf("thread on a file %+v", f)
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
