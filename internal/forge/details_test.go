package forge

import (
	"slices"
	"testing"
	"time"
)

const detailedAnswer = `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{
"number":7,"title":"Add a thing","url":"https://github.com/o/r/pull/7","body":"Why","state":"OPEN","isDraft":false,
"author":{"login":"ann"},"baseRefName":"main","headRefName":"thing",
"baseRefOid":"1111111111111111111111111111111111111111","headRefOid":"2222222222222222222222222222222222222222",
"reviewDecision":"REVIEW_REQUIRED","createdAt":"2026-11-10T09:00:00Z","additions":3,"deletions":1,
"mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED",
"labels":{"nodes":[{"name":"bug","color":"D73A4A"},{"name":"odd","color":"red"}]},
"assignees":{"nodes":[{"login":"ann"}]},
"reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"User","login":"bob"}},{"requestedReviewer":{"__typename":"Team","name":"core"}},{"requestedReviewer":null}]},
"latestReviews":{"nodes":[{"author":{"login":"bob"},"state":"CHANGES_REQUESTED"},{"author":{"login":"cat"},"state":"APPROVED"},{"author":{"login":"me"},"state":"PENDING"},{"author":{"login":"core"},"state":"COMMENTED"}]},
"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE","contexts":{"totalCount":7,"nodes":[
 {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://github.com/o/r/actions/runs/1/job/2",
  "startedAt":"2026-11-11T10:00:00Z","completedAt":"2026-11-11T10:02:30Z","isRequired":true,"checkSuite":{"workflowRun":{"workflow":{"name":"CI"}}}},
 {"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"TIMED_OUT","detailsUrl":"javascript:alert(1)",
  "startedAt":"2026-11-11T10:00:00Z","completedAt":"2026-11-11T11:00:00Z","isRequired":false,"checkSuite":{"workflowRun":null}},
 {"__typename":"CheckRun","name":"lint","status":"IN_PROGRESS","conclusion":null,"detailsUrl":"https://ci.example/3","startedAt":"2026-11-11T10:00:00Z","completedAt":null,"isRequired":false,"checkSuite":null},
 {"__typename":"CheckRun","name":"deploy","status":"QUEUED","conclusion":null,"detailsUrl":null,"startedAt":null,"completedAt":null,"isRequired":false,"checkSuite":null},
 {"__typename":"CheckRun","name":"docs","status":"COMPLETED","conclusion":"SKIPPED","detailsUrl":null,"startedAt":"2026-11-11T10:00:00Z","completedAt":"2026-11-11T10:00:00Z","isRequired":false,"checkSuite":null},
 {"__typename":"StatusContext","context":"ci/external","state":"ERROR","targetUrl":"http://ci.example/4","isRequired":true}]}}}}]},
"timelineItems":{"pageInfo":{"hasPreviousPage":true},"nodes":[
 {"__typename":"PullRequestCommit","commit":{"abbreviatedOid":"aaaaaaa","messageHeadline":"One","committedDate":"2026-11-10T08:00:00Z","author":{"name":"Ann","user":{"login":"ann"}}}},
 {"__typename":"PullRequestCommit","commit":{"abbreviatedOid":"bbbbbbb","messageHeadline":"Two","committedDate":"2026-11-10T08:30:00Z","author":{"name":"Ann","user":{"login":"ann"}}}},
 {"__typename":"PullRequestCommit","commit":{"abbreviatedOid":"ccccccc","messageHeadline":"Three","committedDate":"2026-11-10T08:40:00Z","author":{"name":"No Account","user":null}}},
 {"__typename":"LabeledEvent","actor":{"login":"bot"},"createdAt":"2026-11-10T09:00:01Z","label":{"name":"bug"}},
 {"__typename":"LabeledEvent","actor":{"login":"bot"},"createdAt":"2026-11-10T09:00:02Z","label":{"name":"needs, review"}},
 {"__typename":"LabeledEvent","actor":{"login":"bot"},"createdAt":"2026-11-10T12:00:00Z","label":{"name":"late"}},
 {"__typename":"UnlabeledEvent","actor":{"login":"bot"},"createdAt":"2026-11-10T12:00:01Z","label":{"name":"late"}},
 {"__typename":"ReviewRequestedEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T12:10:00Z","requestedReviewer":{"__typename":"Team","name":"core"}},
 {"__typename":"ReviewRequestedEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T12:10:00Z","requestedReviewer":null},
 {"__typename":"HeadRefForcePushedEvent","actor":null,"createdAt":"2026-11-10T13:00:00Z","beforeCommit":{"abbreviatedOid":"ccccccc"},"afterCommit":{"abbreviatedOid":"ddddddd"}},
 {"__typename":"RenamedTitleEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T13:10:00Z","previousTitle":"Add thing","currentTitle":"Add a thing"},
 {"__typename":"ConvertToDraftEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T13:20:00Z"},
 {"__typename":"ReadyForReviewEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T13:30:00Z"},
 {"__typename":"ClosedEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T13:40:00Z"},
 {"__typename":"ReopenedEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T13:50:00Z"},
 {"__typename":"SomethingNew","actor":{"login":"ann"},"createdAt":"2026-11-10T13:55:00Z"},
 {"__typename":"MergedEvent","actor":{"login":"cat"},"createdAt":"2026-11-10T14:00:00Z"}]},
"comments":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]}}}}}`

// A pull request's checks, whether it can be merged, its labels, who
// reviews it and what happened to it are read with it.
func TestParseReviewDetails(t *testing.T) {
	rv, err := parseReview([]byte(detailedAnswer))
	if err != nil {
		t.Fatal(err)
	}
	if rv.Mergeable != MergeBlocked || rv.Checks != ChecksFailing || !rv.ChecksMore || !rv.More {
		t.Errorf("mergeable %d, checks %v, more %v %v", rv.Mergeable, rv.Checks, rv.ChecksMore, rv.More)
	}
	if !slices.Equal(rv.Labels, []Label{{"bug", "d73a4a"}, {"odd", ""}}) || !slices.Equal(rv.Assignees, []string{"ann"}) {
		t.Errorf("labels %+v, assignees %v", rv.Labels, rv.Assignees)
	}
	// Bob, asked again after asking for changes, is awaited; a review of
	// one's own not sent is no verdict; the team core being asked hides
	// nothing of the user core.
	want := []Reviewer{{"bob", false, ReviewAwaited}, {"core", true, ReviewAwaited}, {"cat", false, ReviewApproved}, {"core", false, ReviewCommented}}
	if !slices.Equal(rv.Reviewers, want) {
		t.Errorf("reviewers %+v", rv.Reviewers)
	}

	type brief struct {
		name, group string
		state       CheckState
		url         string
		required    bool
	}
	var checks []brief
	for _, c := range rv.CheckList {
		checks = append(checks, brief{c.Name, c.Group, c.State, c.URL, c.Required})
	}
	// An address that is no page, or not https, is not opened.
	if !slices.Equal(checks, []brief{
		{"build", "CI", CheckPassed, "https://github.com/o/r/actions/runs/1/job/2", true},
		{"test", "", CheckFailed, "", false},
		{"lint", "", CheckRunning, "https://ci.example/3", false},
		{"deploy", "", CheckWaiting, "", false},
		{"docs", "", CheckSkipped, "", false},
		{"ci/external", "", CheckFailed, "", true},
	}) {
		t.Errorf("checks %+v", checks)
	}
	if b := rv.CheckList[0]; b.Ended.Sub(b.Started) != 150*time.Second || !rv.CheckList[2].Ended.IsZero() || !rv.CheckList[3].Started.IsZero() {
		t.Errorf("times %+v", rv.CheckList)
	}

	type what struct {
		kind          EventKind
		actor         string
		subject, from string
		commits       int
		labels        string
	}
	var events []what
	for _, e := range rv.Events {
		labels := ""
		for _, l := range e.Labels {
			labels += "[" + l + "]"
		}
		events = append(events, what{e.Kind, e.Actor, e.Subject, e.From, len(e.Commits), labels})
	}
	// The commits of one author in a row are one event, and so are the
	// labels one actor sets together; what is not known is left out.
	if !slices.Equal(events, []what{
		{EventCommits, "ann", "", "", 2, ""},
		{EventCommits, "No Account", "", "", 1, ""},
		{EventLabeled, "bot", "", "", 0, "[bug][needs, review]"},
		{EventLabeled, "bot", "", "", 0, "[late]"},
		{EventUnlabeled, "bot", "", "", 0, "[late]"},
		{EventReviewRequested, "ann", "core", "", 0, ""},
		{EventForcePushed, "ghost", "ddddddd", "ccccccc", 0, ""},
		{EventRenamed, "ann", "Add a thing", "Add thing", 0, ""},
		{EventDrafted, "ann", "", "", 0, ""},
		{EventReady, "ann", "", "", 0, ""},
		{EventClosed, "ann", "", "", 0, ""},
		{EventReopened, "ann", "", "", 0, ""},
		{EventMerged, "cat", "", "", 0, ""},
	}) {
		t.Errorf("events %+v", events)
	}
	if e := rv.Events[0]; e.Commits[1] != (EventCommit{"bbbbbbb", "Two"}) || e.At.IsZero() {
		t.Errorf("commits %+v", e)
	}
}

func TestMergeable(t *testing.T) {
	for _, c := range []struct {
		mergeable, status string
		want              Mergeable
	}{
		{"MERGEABLE", "CLEAN", MergeReady}, {"MERGEABLE", "HAS_HOOKS", MergeReady}, {"CONFLICTING", "DIRTY", MergeConflicts},
		{"MERGEABLE", "BEHIND", MergeBehind}, {"MERGEABLE", "BLOCKED", MergeBlocked}, {"MERGEABLE", "UNSTABLE", MergeUnstable},
		{"MERGEABLE", "DRAFT", MergeDraft}, {"UNKNOWN", "UNKNOWN", MergeUnknown}, {"CONFLICTING", "UNKNOWN", MergeConflicts}, {"", "", MergeUnknown},
	} {
		if got := githubMergeable(c.mergeable, c.status); got != c.want {
			t.Errorf("GitHub %s %s: %d, want %d", c.mergeable, c.status, got, c.want)
		}
	}
	for _, c := range []struct {
		status    string
		conflicts bool
		want      Mergeable
	}{
		{"MERGEABLE", false, MergeReady}, {"CONFLICT", true, MergeConflicts}, {"NEED_REBASE", false, MergeBehind},
		{"DRAFT_STATUS", false, MergeDraft}, {"CI_MUST_PASS", false, MergeUnstable}, {"CI_STILL_RUNNING", false, MergeUnstable},
		{"NOT_APPROVED", false, MergeBlocked}, {"DISCUSSIONS_NOT_RESOLVED", false, MergeBlocked}, {"SOMETHING_NEW", false, MergeBlocked},
		{"CHECKING", false, MergeUnknown}, {"CHECKING", true, MergeConflicts}, {"", false, MergeUnknown},
	} {
		if got := gitlabMergeable(c.status, c.conflicts); got != c.want {
			t.Errorf("GitLab %s %v: %d, want %d", c.status, c.conflicts, got, c.want)
		}
	}
}

func TestWebURL(t *testing.T) {
	for given, ok := range map[string]bool{
		"https://ci.example/run/1": true, "https://github.com/o/r/actions/runs/1": true,
		"http://ci.example/run/1": false, "https://user@ci.example/": false, "https://user:pass@ci.example/": false,
		"javascript:alert(1)": false, "file:///etc/passwd": false, "https:///path": false, "mailto:a@b.c": false, "": false, "/o/r/-/jobs/1": false,
		"https://ci.example/\x7f": false,
	} {
		if got := WebURL(given); (got == given && given != "") != ok {
			t.Errorf("%q: %q", given, got)
		}
	}
}

func TestHexColor(t *testing.T) {
	for given, want := range map[string]string{"D73A4A": "d73a4a", "#a8d695": "a8d695", "red": "", "#fff": "", "12345g": "", "": "", "#1234567": ""} {
		if got := hexColor(given); got != want {
			t.Errorf("%q: %q, want %q", given, got, want)
		}
	}
}

const glDetailedAnswer = `{"data":{"currentUser":{"username":"me"},"project":{"mergeRequest":{
"iid":"12","title":"Add","webUrl":"https://gitlab.com/g/p/-/merge_requests/12","description":"Why","state":"opened","draft":false,
"author":{"username":"ann"},"sourceBranch":"add","targetBranch":"main","createdAt":"2026-11-10T09:00:00Z","approved":false,
"detailedMergeStatus":"CI_STILL_RUNNING","conflicts":false,
"labels":{"nodes":[{"title":"type::bug","color":"#CC0000"}]},
"assignees":{"nodes":[{"username":"ann"}]},
"reviewers":{"nodes":[
 {"username":"bob","mergeRequestInteraction":{"reviewState":"REQUESTED_CHANGES","approved":false}},
 {"username":"cat","mergeRequestInteraction":{"reviewState":"REVIEWED","approved":true}},
 {"username":"dan","mergeRequestInteraction":{"reviewState":"REVIEWED","approved":false}},
 {"username":"eve","mergeRequestInteraction":{"reviewState":"UNREVIEWED","approved":false}},
 {"username":"fay","mergeRequestInteraction":null}]},
"headPipeline":{"status":"RUNNING","jobs":{"count":7,"nodes":[
 {"name":"unit","status":"SUCCESS","stage":{"name":"test"},"webPath":"/g/p/-/jobs/1","startedAt":"2026-11-11T10:00:00Z","finishedAt":"2026-11-11T10:05:00Z","allowFailure":false},
 {"name":"lint","status":"FAILED","stage":{"name":"test"},"webPath":"//evil.example/x","startedAt":"2026-11-11T10:00:00Z","finishedAt":"2026-11-11T10:01:00Z","allowFailure":true},
 {"name":"e2e","status":"RUNNING","stage":{"name":"test"},"webPath":"https://evil.example/x","startedAt":"2026-11-11T10:00:00Z","finishedAt":null,"allowFailure":false},
 {"name":"deploy","status":"MANUAL","stage":null,"webPath":null,"startedAt":null,"finishedAt":null,"allowFailure":true},
 {"name":"old","status":"CANCELED","stage":{"name":"build"},"webPath":"/g/p/-/jobs/5","startedAt":null,"finishedAt":null,"allowFailure":false},
 {"name":"docs","status":"SKIPPED","stage":{"name":"build"},"webPath":"/g/p/-/jobs/6","startedAt":null,"finishedAt":null,"allowFailure":false}]}},
"diffStatsSummary":{"additions":5,"deletions":0},
"diffRefs":{"baseSha":"1111111111111111111111111111111111111111","startSha":"3333333333333333333333333333333333333333","headSha":"2222222222222222222222222222222222222222"},
"userPermissions":{"createNote":true},"approvedBy":{"nodes":[{"username":"cat"},{"username":"zed"}]},
"notes":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}}`

// A merge request's jobs, whether it can be merged, its labels and its
// reviewers are read with it.
func TestParseGitLabReviewDetails(t *testing.T) {
	rv, err := parseGitLabReview([]byte(glDetailedAnswer))
	if err != nil {
		t.Fatal(err)
	}
	jobPages(Repo{GitLab, "gitlab.com", "g/p"}, rv.CheckList)
	if rv.Mergeable != MergeUnstable || !rv.ChecksMore || len(rv.Events) != 0 {
		t.Errorf("mergeable %d, more checks %v, events %+v", rv.Mergeable, rv.ChecksMore, rv.Events)
	}
	if !slices.Equal(rv.Labels, []Label{{"type::bug", "cc0000"}}) || !slices.Equal(rv.Assignees, []string{"ann"}) {
		t.Errorf("labels %+v, assignees %v", rv.Labels, rv.Assignees)
	}
	// Zed approved without being asked to review.
	want := []Reviewer{{"bob", false, ReviewChangesRequested}, {"cat", false, ReviewApproved}, {"dan", false, ReviewCommented},
		{"eve", false, ReviewAwaited}, {"fay", false, ReviewAwaited}, {"zed", false, ReviewApproved}}
	if !slices.Equal(rv.Reviewers, want) {
		t.Errorf("reviewers %+v", rv.Reviewers)
	}
	type brief struct {
		name, group string
		state       CheckState
		url         string
		required    bool
	}
	var checks []brief
	for _, c := range rv.CheckList {
		checks = append(checks, brief{c.Name, c.Group, c.State, c.URL, c.Required})
	}
	// A job's page is a path on the merge request's host, and no other.
	if !slices.Equal(checks, []brief{
		{"unit", "test", CheckPassed, "https://gitlab.com/g/p/-/jobs/1", true},
		{"lint", "test", CheckFailed, "", false},
		{"e2e", "test", CheckRunning, "", true},
		{"deploy", "", CheckWaiting, "", false},
		{"old", "build", CheckCancelled, "https://gitlab.com/g/p/-/jobs/5", true},
		{"docs", "build", CheckSkipped, "https://gitlab.com/g/p/-/jobs/6", true},
	}) {
		t.Errorf("jobs %+v", checks)
	}
	if u := rv.CheckList[0]; u.Ended.Sub(u.Started) != 5*time.Minute {
		t.Errorf("times %+v", u)
	}
}
