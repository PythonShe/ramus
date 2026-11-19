package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/forge"
	"ramus/internal/git/gittest"
)

// The pull requests, their checks and the repository picker keep a row's
// state with its item, not its index: a menu opened on a row acts on that
// row's item when rows come in above it, as a list read again does.

// pullHost is a repository with pull request 7, and a clone of it that
// reaches it as GitHub's o/r.
func pullHost(t *testing.T, h *harness) (r *gittest.Repo, head string) {
	host := gittest.Init(t)
	host.Write("a.txt", "1\n")
	host.Commit("base")
	host.Git("checkout", "-q", "-b", "thing")
	host.Write("a.txt", "2\n")
	head = host.Commit("head")
	host.Git("update-ref", "refs/pull/7/head", head)
	host.Git("checkout", "-q", "main")
	r = h.repoWith()
	r.Git("remote", "add", "origin", "https://github.com/o/r")
	r.Git("config", "url."+host.Dir+".insteadOf", "https://github.com/o/r")
	return r, head
}

// pullNode is a pull request as GitHub lists it.
func pullNode(n, title string) string {
	return `{"id":"PR_` + n + `","number":` + n + `,"title":"` + title + `","url":"https://github.com/o/r/pull/` + n + `",
"isDraft":false,"state":"OPEN","author":{"login":"ann"},"headRefName":"thing","baseRefName":"main",
"updatedAt":"2026-11-13T16:04:48Z","headRepositoryOwner":{"login":"o"}}`
}

func pullList(nodes ...string) string {
	return `{"data":{"repository":{"pullRequests":{"totalCount":` + strconv.Itoa(len(nodes)) + `,"nodes":[` + strings.Join(nodes, ",") + `]}}}}`
}

// toolDir is the folder of the fake tool reviewTool made, which holds
// what it answers.
func toolDir(env []string) string {
	return strings.Split(strings.TrimPrefix(env[0], "PATH="), ":")[0]
}

func TestPullRequestMenuFollowsItsPull(t *testing.T) {
	h := newHarness(t)
	r, _ := pullHost(t, h)
	env, _ := reviewTool(t, "gh", "pullRequests(states", "pullRequest(number", pullList(pullNode("7", "Add a thing")), "{}")
	tools := &forgeTools{env: env}
	h.app.forgeFind = tools.find
	h.app.Settings.PullRequests = true
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the pull requests in the sidebar", h.shows("Pull Requests"))
	h.click("Pull Requests")
	h.until("the list", h.shows("Add a thing"))
	ps := &h.app.current().pr
	h.until("the list and its details read", func() bool { return !ps.listing })
	// A pull request opened since is listed first as the list is read
	// again.
	h.rightClickThen("Add a thing", func() {
		if err := os.WriteFile(filepath.Join(toolDir(env), "list.json"), []byte(pullList(pullNode("9", "Newer"), pullNode("7", "Add a thing"))), 0o644); err != nil {
			t.Fatal(err)
		}
		ps.listAt = time.Time{}
	}, func() bool { return len(ps.list) == 2 && ps.list[0].Number == 9 }, "Open in Browser")
	h.until("a page opened", func() bool { return len(h.tt.OpenedURLs()) > 0 })
	if got := h.tt.OpenedURLs(); !slices.Equal(got, []string{"https://github.com/o/r/pull/7"}) {
		t.Fatalf("Open in Browser on #7 opened %q", got)
	}
}

func TestCheckMenuFollowsItsCheck(t *testing.T) {
	h := newHarness(t)
	r, head := pullHost(t, h)
	review := `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{"number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","body":"","state":"OPEN","isDraft":false,"author":{"login":"ann"},
"baseRefName":"main","headRefName":"thing","baseRefOid":"","headRefOid":"` + head + `","reviewDecision":"REVIEW_REQUIRED",
"createdAt":"2026-11-10T09:00:00Z","additions":1,"deletions":1,
"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE","contexts":{"totalCount":2,"nodes":[
 {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://github.com/o/r/actions/runs/1/job/1",
  "startedAt":"2026-11-11T10:00:00Z","completedAt":"2026-11-11T10:02:30Z","isRequired":true,"checkSuite":{"workflowRun":{"workflow":{"name":"CI"}}}},
 {"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/o/r/actions/runs/1/job/2",
  "startedAt":"2026-11-11T10:00:00Z","completedAt":"2026-11-11T10:01:00Z","isRequired":false,"checkSuite":{"workflowRun":{"workflow":{"name":"CI"}}}}]}}}}]},
"comments":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]}}}}}`
	env, _ := reviewTool(t, "gh", "pullRequests(states", "pullRequest(number", pullList(pullNode("7", "Add a thing")), review)
	tools := &forgeTools{env: env}
	h.app.forgeFind = tools.find
	h.app.Settings.PullRequests = true
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the pull requests in the sidebar", h.shows("Pull Requests"))
	h.click("Pull Requests")
	h.until("the list", h.shows("Add a thing"))
	h.click("Add a thing")
	h.until("the pull request", h.shows("ann wants to merge thing into main"))
	rv := h.app.current().pr.rv
	h.click("Checks (2)")
	h.until("the checks", func() bool { _, ok := h.tt.Access("build, Passed"); return ok })
	// A check that failed since is listed first as the review is read
	// again: build moves down a row.
	h.rightClickThen("build", func() {
		lint := forge.Check{Name: "lint", State: forge.CheckFailed, URL: "https://github.com/o/r/actions/runs/1/job/3"}
		rv.checks = append([]forge.Check{lint}, rv.checks...)
	}, func() bool { _, ok := h.tt.Access("lint, Failed"); return ok }, "Open in Browser")
	h.until("a page opened", func() bool { return len(h.tt.OpenedURLs()) > 0 })
	if got := h.tt.OpenedURLs(); !slices.Equal(got, []string{"https://github.com/o/r/actions/runs/1/job/1"}) {
		t.Fatalf("Open in Browser on build opened %q", got)
	}
}

func TestRepoPickerMenuFollowsItsRepository(t *testing.T) {
	h := newHarness(t)
	one, two, other := h.repoWith(), h.repoWith(), h.repoWith()
	for i, r := range []*gittest.Repo{one, two} {
		h.trust.Set(r.GitDir(), true)
		h.lib.Opened(r.Dir, time.Unix(1_700_000_000+int64(i), 0))
	}
	h.lib.Rename(two.Dir, "Second")
	h.app.Open(one.Dir)
	h.app.Open(two.Dir)
	h.until("both open", func() bool { return len(h.app.tabs) == 2 })
	h.app.openRepoPicker()
	h.until("the picker", func() bool { _, ok := h.tt.Find("Second"); return ok })
	// A repository opened in another window comes in above it, under
	// Recent.
	h.rightClickThen("Second", func() {
		h.lib.Opened(other.Dir, time.Unix(1_700_000_100, 0))
	}, func() bool { return hasExact(h, "Recent") }, "Close")
	h.until("a tab closed", func() bool { return len(h.app.tabs) == 1 })
	if !h.app.tabs[0].is(cleanPath(one.Dir)) {
		t.Fatalf("Close on %s closed another; left %s", two.Dir, h.app.tabs[0].s.Path)
	}
}
