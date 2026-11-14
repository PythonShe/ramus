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

// With gh's stack extension, branches stacked here become a GitHub stack
// through gh stack init, shown in their rows from the file it keeps, and
// the stack of the branch checked out is submitted through it.
func TestGitHubStack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	h := newHarness(t)
	r := h.repoWith()
	r.Git("remote", "add", "origin", "https://github.com/o/r.git")
	for _, b := range []string{"a", "b"} {
		r.Git("checkout", "-q", "-b", b)
		r.Write(b+".txt", b)
		r.Commit(b)
		r.Git("update-ref", "refs/remotes/origin/"+b, "HEAD")
		r.Git("config", "branch."+b+".remote", "origin")
		r.Git("config", "branch."+b+".merge", "refs/heads/"+b)
	}
	r.Git("checkout", "-q", "main")
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	stackJSON := `{"schemaVersion":1,"repository":"github.com/o/r","stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"a"},{"branch":"b","pullRequest":{"number":12,"url":"https://github.com/o/r/pull/12"}}]}]}`
	script := `#!/bin/sh
echo "$@" >> '` + log + `'
case "$1 $2" in
"stack --version") echo "gh stack version 0.1.0" ;;
"stack init") printf '%s' '` + stackJSON + `' > "$(git rev-parse --git-dir)/gh-stack"; git checkout -q b ;;
"stack submit") echo "Stack submitted" ;;
"api graphql") echo '{"data":{"repository":{}}}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := &forgeTools{env: []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}}
	h.app.forgeFind = tools.find
	h.app.Settings.PullRequests = true
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("Branches and Tags"))
	h.click("Branches and Tags")
	h.until("gh stack found", func() bool { return h.app.ghStack.ok })
	calls := func() string { b, _ := os.ReadFile(log); return string(b) }

	h.until("the stack found here", func() bool { info := h.app.current().refs.info; return info != nil && len(info.Stacks) == 1 })
	if err := h.tt.RightClick("b"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Create GitHub Stack of 2 Branches…"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", h.shows("Create a GitHub stack?"))
	h.click("Create")
	h.until("gh stack init", func() bool { return strings.Contains(calls(), "stack init -- a b") })
	tb := h.app.current()
	h.until("b checked out", func() bool { return tb.s.State.Head.Target == "refs/heads/b" })
	h.until("the stack shown", h.shows("Branch 2 of 2 in a GitHub stack"))
	if !h.tt.HasText("#12") {
		t.Fatalf("no pull request from the stack's file: %q", h.tt.Texts())
	}

	if err := h.tt.RightClick("a"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Submit GitHub Stack…"); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", h.shows("Submit the stack?"))
	h.click("Submit")
	h.until("gh stack submit", func() bool { return strings.Contains(calls(), "stack submit --auto") })
	h.idle(tb, "the submit")
}
