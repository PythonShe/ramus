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

func TestParseGitLabOpen(t *testing.T) {
	out := `{"data":{"project":{"mergeRequests":{"count":4,"nodes":[
{"iid":"12","title":"Add","webUrl":"https://gitlab.com/g/p/-/merge_requests/12","draft":false,"state":"opened","author":{"username":"ann"},
 "sourceBranch":"add","targetBranch":"main","updatedAt":"2026-11-13T16:04:48Z","approved":false,"detailedMergeStatus":"REQUESTED_CHANGES","headPipeline":{"status":"FAILED"},
 "diffStatsSummary":{"additions":40,"deletions":2}},
{"iid":"x","title":"Bad iid"},
{"iid":"9","title":"Ok","draft":true,"state":"opened","author":null,"approved":true,"detailedMergeStatus":"MERGEABLE","headPipeline":null}]}}}}`
	list, more, err := parseGitLabOpen([]byte(out), "g/p")
	if err != nil || !more || len(list) != 2 {
		t.Fatalf("%+v %v %v", list, more, err)
	}
	if a := list[0]; a.Number != 12 || a.Author != "ann" || a.Base != "main" || a.Decision != ReviewChangesRequested || a.Checks != ChecksFailing ||
		a.Added != 40 || a.Deleted != 2 {
		t.Errorf("first %+v", a)
	}
	if b := list[1]; b.Author != "ghost" || !b.Draft || b.Decision != ReviewApproved {
		t.Errorf("second %+v", b)
	}
	if _, _, err := parseGitLabOpen([]byte(`{"data":{"project":null}}`), "g/p"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no project: %v", err)
	}
}

const glReviewAnswer = `{"data":{"currentUser":{"username":"me"},"project":{"mergeRequest":{
"iid":"12","title":"Add","webUrl":"https://gitlab.com/g/p/-/merge_requests/12","description":"Why","state":"opened","draft":false,
"author":{"username":"ann"},"sourceBranch":"add","targetBranch":"main","createdAt":"2026-11-10T09:00:00Z","approved":false,
"detailedMergeStatus":"NOT_APPROVED","headPipeline":null,"diffStatsSummary":{"additions":5,"deletions":0},
"diffRefs":{"baseSha":"1111111111111111111111111111111111111111","startSha":"3333333333333333333333333333333333333333","headSha":"2222222222222222222222222222222222222222"},
"userPermissions":{"createNote":true},"approvedBy":{"nodes":[{"username":"cat"}]},
"notes":{"pageInfo":{"hasNextPage":false},"nodes":[
 {"id":"gid://gitlab/Note/1","author":{"username":"bob"},"body":"Looks fine","createdAt":"2026-11-11T09:00:00Z","system":false,
  "discussion":{"id":"gid://gitlab/Discussion/aa","resolvable":false,"resolved":false,"userPermissions":{"resolveNote":false}},"position":null},
 {"id":"gid://gitlab/Note/2","author":{"username":"bob"},"body":"Why here?","createdAt":"2026-11-11T10:00:00Z","system":false,
  "discussion":{"id":"gid://gitlab/Discussion/bb","resolvable":true,"resolved":false,"userPermissions":{"resolveNote":true}},
  "position":{"positionType":"text","filePath":"a.go","oldPath":"a.go","newPath":"a.go","oldLine":10,"newLine":12,"diffRefs":{"headSha":"2222222222222222222222222222222222222222"}}},
 {"id":"gid://gitlab/Note/3","author":{"username":"me"},"body":"Gone","createdAt":"2026-11-11T11:00:00Z","system":false,
  "discussion":{"id":"gid://gitlab/Discussion/cc","resolvable":true,"resolved":true,"userPermissions":{"resolveNote":true}},
  "position":{"positionType":"text","filePath":"b.go","oldPath":"b.go","newPath":"b.go","oldLine":3,"newLine":null,"diffRefs":{"headSha":"4444444444444444444444444444444444444444"}}},
 {"id":"gid://gitlab/Note/4","author":{"username":"ann"},"body":"Because","createdAt":"2026-11-11T12:00:00Z","system":false,
  "discussion":{"id":"gid://gitlab/Discussion/bb","resolvable":true,"resolved":false,"userPermissions":{"resolveNote":true}},
  "position":{"positionType":"text","filePath":"a.go","oldPath":"a.go","newPath":"a.go","oldLine":10,"newLine":12,"diffRefs":{"headSha":"2222222222222222222222222222222222222222"}}}]}}}}}`

func TestParseGitLabReview(t *testing.T) {
	rv, err := parseGitLabReview([]byte(glReviewAnswer))
	if err != nil {
		t.Fatal(err)
	}
	if rv.Added != 5 || rv.Deleted != 0 {
		t.Errorf("lines +%d -%d", rv.Added, rv.Deleted)
	}
	if rv.Number != 12 || rv.Viewer != "me" || rv.Author != "ann" || rv.Base != "main" || rv.Body != "Why" || rv.Decision != ReviewRequired ||
		rv.BaseOID[0] != '1' || rv.StartOID[0] != '3' || rv.HeadOID[0] != '2' || rv.More {
		t.Errorf("merge request %+v", rv)
	}
	if len(rv.Comments) != 1 || rv.Comments[0].Body != "Looks fine" || len(rv.Reviews) != 1 || rv.Reviews[0].Author != "cat" || rv.Reviews[0].State != ReviewApproved {
		t.Errorf("conversation %+v %+v", rv.Comments, rv.Reviews)
	}
	if len(rv.Threads) != 2 {
		t.Fatalf("threads %+v", rv.Threads)
	}
	a, b := rv.Threads[0], rv.Threads[1]
	// An unchanged line is on the new side, by its new number; the reply
	// joins its thread.
	if a.ID != "gid://gitlab/Discussion/bb" || a.Path != "a.go" || a.Side != Right || a.Line != 12 || a.Outdated || !a.CanResolve || a.CanUnresolve ||
		!a.CanReply || len(a.Comments) != 2 || a.Comments[1].Body != "Because" {
		t.Errorf("first thread %+v", a)
	}
	// On a removed line, of an older head.
	if b.Side != Left || !b.Outdated || b.Line != 0 || b.OriginalLine != 3 || !b.Resolved || b.CanResolve || !b.CanUnresolve {
		t.Errorf("outdated thread %+v", b)
	}
}

// Notes on a whole file, as GitLab gives them (positionType file, and
// image for a point of an image, with no line), are threads on their
// file, as a GitHub comment on a file is; one of an older head is on a
// file changed since.
func TestParseGitLabFileNotes(t *testing.T) {
	const head = `"diffRefs":{"headSha":"2222222222222222222222222222222222222222"}`
	out := `{"data":{"currentUser":{"username":"me"},"project":{"mergeRequest":{
"iid":"12","title":"Add","state":"opened","author":{"username":"ann"},"sourceBranch":"add","targetBranch":"main",
"diffRefs":{"baseSha":"1111111111111111111111111111111111111111","startSha":"3333333333333333333333333333333333333333","headSha":"2222222222222222222222222222222222222222"},
"userPermissions":{"createNote":true},
"notes":{"pageInfo":{"hasNextPage":false},"nodes":[
 {"id":"gid://gitlab/Note/1","author":{"username":"bob"},"body":"Split this file","createdAt":"2026-11-11T09:00:00Z","system":false,
  "discussion":{"id":"gid://gitlab/Discussion/aa","resolvable":true,"resolved":false,"userPermissions":{"resolveNote":true}},
  "position":{"positionType":"file","filePath":"c.go","oldPath":"c.go","newPath":"c.go","oldLine":null,"newLine":null,` + head + `}},
 {"id":"gid://gitlab/Note/2","author":{"username":"ann"},"body":"Will do","createdAt":"2026-11-11T10:00:00Z","system":false,
  "discussion":{"id":"gid://gitlab/Discussion/aa","resolvable":true,"resolved":false,"userPermissions":{"resolveNote":true}},
  "position":{"positionType":"file","filePath":"c.go","oldPath":"c.go","newPath":"c.go","oldLine":null,"newLine":null,` + head + `}},
 {"id":"gid://gitlab/Note/3","author":{"username":"bob"},"body":"Too dark","createdAt":"2026-11-11T11:00:00Z","system":false,
  "discussion":{"id":"gid://gitlab/Discussion/bb","resolvable":true,"resolved":false,"userPermissions":{"resolveNote":true}},
  "position":{"positionType":"image","filePath":"logo.png","oldPath":"logo.png","newPath":"logo.png","oldLine":null,"newLine":null,
   "diffRefs":{"headSha":"4444444444444444444444444444444444444444"}}}]}}}}}`
	rv, err := parseGitLabReview([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.Comments) != 0 || len(rv.Threads) != 2 {
		t.Fatalf("comments %+v threads %+v", rv.Comments, rv.Threads)
	}
	f, img := rv.Threads[0], rv.Threads[1]
	if !f.File || f.Path != "c.go" || f.Line != 0 || f.OriginalLine != 0 || f.Outdated || !f.CanResolve || !f.CanReply ||
		len(f.Comments) != 2 || f.Comments[1].Body != "Will do" {
		t.Errorf("thread on a file %+v", f)
	}
	if !img.File || img.Path != "logo.png" || img.Line != 0 || img.OriginalLine != 0 || !img.Outdated {
		t.Errorf("thread on an image of an older head %+v", img)
	}
}

// glabLog is a glab that logs each call's arguments and input, answers
// a draft note made with its number, and fails the call whose arguments
// or input hold fail, with 409 Conflict or, for fail as "pattern=>message",
// with that message.
func glabLog(t *testing.T, fail string) (env []string, log func() string) {
	t.Helper()
	msg := "glab: 409 Conflict"
	if p, m, ok := strings.Cut(fail, "=>"); ok {
		fail, msg = p, m
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
in=$(cat)
printf '%s\n%s\n' "$*" "$in" >> '` + dir + `/log'
case "$* $in" in
*'` + fail + `'*) echo '` + msg + `' >&2; exit 1 ;;
*"POST projects/g%2Fp/merge_requests/12/draft_notes "*) n=$(grep -c 'draft_notes ' '` + dir + `/log'); echo "{\"id\":$n}" ;;
*) echo '{}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + ":/usr/bin:/bin"}, func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "log"))
		return string(b)
	}
}

func TestGitLabSubmit(t *testing.T) {
	r := Repo{GitLab, "gitlab.com", "g/p"}
	at := Revs{Base: strings.Repeat("1", 40), Start: strings.Repeat("3", 40), Head: strings.Repeat("2", 40)}
	drafts := []Draft{
		{Path: "a.go", Side: Right, Line: 12, Across: 10, Body: "unchanged"},
		{Path: "b.go", OldPath: "old.go", Side: Left, Line: 5, Body: "removed"},
	}
	env, log := glabLog(t, "never")
	c, err := Find(env, GitLab)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictRequestChanges, "Please", drafts); err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(log()), "\n")
	if len(calls) != 6 {
		t.Fatalf("calls %q", calls)
	}
	var p struct {
		Note     string
		Position map[string]any
	}
	if !strings.Contains(calls[0], "--method POST projects/g%2Fp/merge_requests/12/draft_notes --input -") {
		t.Errorf("first call %q", calls[0])
	}
	json.Unmarshal([]byte(calls[1]), &p)
	if pos := p.Position; p.Note != "unchanged" || pos["new_line"] != 12.0 || pos["old_line"] != 10.0 || pos["old_path"] != "a.go" ||
		pos["base_sha"] != at.Base || pos["start_sha"] != at.Start || pos["head_sha"] != at.Head || pos["position_type"] != "text" {
		t.Errorf("unchanged line %s", calls[1])
	}
	p.Position = nil
	json.Unmarshal([]byte(calls[3]), &p)
	if pos := p.Position; pos["old_line"] != 5.0 || pos["new_line"] != nil || pos["old_path"] != "old.go" || pos["new_path"] != "b.go" {
		t.Errorf("removed line %s", calls[3])
	}
	if !strings.Contains(calls[4], "draft_notes/bulk_publish") || calls[5] != `{"note":"Please","reviewer_state":"requested_changes"}` {
		t.Errorf("publish %q %q", calls[4], calls[5])
	}

	// An approval with no comments is the summary, then the approval.
	env, log = glabLog(t, "never")
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictApprove, "Good", nil); err != nil {
		t.Fatal(err)
	}
	if got := log(); !strings.Contains(got, "merge_requests/12/notes --input -\n{\"body\":\"Good\"}") ||
		!strings.Contains(got, "merge_requests/12/approve --input -\n{\"sha\":\""+at.Head+"\"}") || strings.Contains(got, "draft_notes") {
		t.Errorf("approval %q", got)
	}

	// A summary alone goes out as a draft published.
	env, log = glabLog(t, "never")
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictComment, "Hm", nil); err != nil {
		t.Fatal(err)
	}
	if got := log(); !strings.Contains(got, "draft_notes --input -\n{\"note\":\"Hm\"}") || !strings.Contains(got, "bulk_publish --input -\n{}") {
		t.Errorf("summary %q", got)
	}

	// A draft that fails takes back those made before it.
	env, log = glabLog(t, `"note":"removed"`)
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictComment, "", drafts); err == nil {
		t.Fatal("a failed draft taken")
	}
	if got := log(); !strings.Contains(got, "--method DELETE projects/g%2Fp/merge_requests/12/draft_notes/1\n") || strings.Contains(got, "bulk_publish") {
		t.Errorf("failure %q", got)
	}

	// An approval of a head moved on.
	env, _ = glabLog(t, "/approve")
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictApprove, "", nil); !errors.Is(err, ErrHeadMoved) {
		t.Errorf("stale approval: %v", err)
	}
	// Comments published before an approval that fails are said to be
	// sent, not to be sent again.
	var sent *SentError
	env, _ = glabLog(t, "/approve")
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictApprove, "", drafts); !errors.As(err, &sent) || !errors.Is(err, ErrHeadMoved) || !sent.NotApproved {
		t.Errorf("approval after comments: %v", err)
	}
	// A publish that fails takes the drafts back.
	env, log = glabLog(t, "bulk_publish")
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictComment, "", drafts); err == nil || errors.As(err, &sent) {
		t.Errorf("failed publish: %v", err)
	}
	if got := log(); !strings.Contains(got, "draft_notes/1\n") || !strings.Contains(got, "draft_notes/2\n") {
		t.Errorf("failed publish left drafts: %q", got)
	}
	// A publish whose answer was lost, its drafts gone as published, is
	// not sent again.
	env, _ = glabLog(t, "draft_notes/=>glab: 404 Not Found")
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictRequestChanges, "", drafts); !errors.As(err, &sent) || sent.NotApproved {
		t.Errorf("publish answer lost: %v", err)
	}
	// One that asked for an approval has it still to send.
	sent = nil
	env, _ = glabLog(t, "draft_notes/=>glab: 404 Not Found")
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictApprove, "", drafts); !errors.As(err, &sent) || !sent.NotApproved {
		t.Errorf("publish answer lost before an approval: %v", err)
	}
	// Drafts it could not take back are told of.
	var left *LeftError
	env, _ = glabLog(t, "draft_notes/")
	c, _ = Find(env, GitLab)
	if err := c.SubmitReview(context.Background(), r, 12, at, VerdictComment, "", drafts); !errors.As(err, &left) || left.Left != 2 {
		t.Errorf("drafts left: %v", err)
	}

	// A comment sent now goes as a discussion of its own: published as
	// a review, it would take the drafts pending in the browser with it.
	env, log = glabLog(t, "never")
	c, _ = Find(env, GitLab)
	if err := c.CommentNow(context.Background(), r, 12, at, drafts[0]); err != nil {
		t.Fatal(err)
	}
	calls = strings.Split(strings.TrimSpace(log()), "\n")
	var d struct {
		Body     string
		Position map[string]any
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "--method POST projects/g%2Fp/merge_requests/12/discussions --input -") {
		t.Fatalf("comment now %q", calls)
	}
	json.Unmarshal([]byte(calls[1]), &d)
	if pos := d.Position; d.Body != "unchanged" || pos["new_line"] != 12.0 || pos["old_line"] != 10.0 || pos["head_sha"] != at.Head || pos["position_type"] != "text" {
		t.Errorf("comment now %s", calls[1])
	}

	env, log = glabLog(t, "never")
	c, _ = Find(env, GitLab)
	th := Thread{ID: "gid://gitlab/Discussion/bb"}
	if err := c.Reply(context.Background(), r, 12, th, "Because"); err != nil {
		t.Fatal(err)
	}
	if err := c.Resolve(context.Background(), r, 12, th, true); err != nil {
		t.Fatal(err)
	}
	if got := log(); !strings.Contains(got, "--method POST projects/g%2Fp/merge_requests/12/discussions/bb/notes --input -\n{\"body\":\"Because\"}") ||
		!strings.Contains(got, "--method PUT projects/g%2Fp/merge_requests/12/discussions/bb --input -\n{\"resolved\":true}") {
		t.Errorf("thread %q", got)
	}
	if err := c.Resolve(context.Background(), r, 12, Thread{ID: "gid://gitlab/Discussion/../x"}, true); err == nil {
		t.Error("a discussion id that is no id taken")
	}
}
