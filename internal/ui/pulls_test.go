package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeGH is a gh that answers with out, or fails with code and out on
// stderr, found through the environment it returns.
func fakeGH(t *testing.T, out string, code int) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + out + "\nEOF\n"
	if code != 0 {
		script = "#!/bin/sh\necho '" + out + "' >&2\nexit " + string(rune('0'+code)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + ":/usr/bin:/bin"}
}

// A branch pushed to GitHub shows its open pull request and checks, as
// gh says, once the settings turn it on; its menu opens the pages, which
// need no gh. A missing login is said once.
func TestPullRequests(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("remote", "add", "origin", "https://github.com/o/r.git")
	r.Git("branch", "feature")
	r.Git("update-ref", "refs/remotes/origin/feature", "HEAD")
	r.Git("config", "branch.feature.remote", "origin")
	r.Git("config", "branch.feature.merge", "refs/heads/feature")
	env := fakeGH(t, `{"data":{"repository":{"b0":{"nodes":[{"number":7,"title":"Add the feature","url":"https://github.com/o/r/pull/7",
		"isDraft":false,"headRefName":"feature","headRepositoryOwner":{"login":"o"},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}}]}}}}`, 0)
	tools := &forgeTools{env: env}
	h.app.forgeFind = tools.find
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("Branches and Tags"))
	h.click("Branches and Tags")
	h.until("the branches", h.shows("feature"))
	tb := h.app.current()
	if tb.pulls.loading || tb.pulls.by != nil {
		t.Fatal("pull requests asked for with the setting off")
	}

	h.app.Settings.PullRequests = true
	h.tt.Frame()
	h.until("the pull request", h.shows("#7"))
	if _, ok := h.tt.Access("checks failed"); !ok {
		t.Fatal("the checks are not named")
	}
	if err := h.tt.RightClick("feature"); err != nil {
		t.Fatal(err)
	}
	menu := strings.Join(h.tt.Menu(), "\n")
	for _, item := range []string{"Open Pull Request #7", "Show Pull Requests in Browser", "Create Pull Request in Browser"} {
		if !strings.Contains(menu, item) {
			t.Fatalf("no %q in %q", item, menu)
		}
	}
	if err := h.tt.ChooseMenuItem("Create Pull Request in Browser"); err != nil {
		t.Fatal(err)
	}
	h.until("the page opened", func() bool {
		return slices.Contains(h.tt.OpenedURLs(), "https://github.com/o/r/compare/feature?expand=1")
	})

	// gh logged out: said once, however often the view asks again.
	tools = &forgeTools{env: fakeGH(t, "To get started with GitHub CLI, please run:  gh auth login", 4)}
	h.app.forgeFind = tools.find
	h.app.Reload()
	h.until("the notice", func() bool { return strings.Contains(h.app.notice, "gh auth login") })
	h.app.notice = ""
	tb.pulls.key = ""
	h.tt.Frame()
	h.until("asked again", func() bool { return !tb.pulls.loading && tb.pulls.key != "" })
	if h.app.notice != "" {
		t.Fatalf("said again: %q", h.app.notice)
	}
}
