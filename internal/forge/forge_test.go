package forge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want Repo
		ok   bool
	}{
		{"https://github.com/owner/repo.git", Repo{GitHub, "github.com", "owner/repo"}, true},
		{"git@github.com:owner/repo.git", Repo{GitHub, "github.com", "owner/repo"}, true},
		{"ssh://git@ssh.github.com:443/owner/repo", Repo{GitHub, "github.com", "owner/repo"}, true},
		{"git@github.com-work:owner/repo.git", Repo{GitHub, "github.com", "owner/repo"}, true},
		{"https://github.example.com/team/tool", Repo{GitHub, "github.example.com", "team/tool"}, true},
		{"https://gitlab.com/group/sub/project.git", Repo{GitLab, "gitlab.com", "group/sub/project"}, true},
		{"git@gitlab.example.org:group/project.git", Repo{GitLab, "gitlab.example.org", "group/project"}, true},
		{"https://github.com/owner/repo/extra", Repo{}, false},
		{"https://github.com/owner", Repo{}, false},
		{"https://example.com/owner/repo", Repo{}, false},
		{"/srv/git/repo.git", Repo{}, false},
		{"https://github.com/owner/../repo", Repo{}, false},
		{"file:///github.com/owner/repo", Repo{}, false},
	} {
		got, ok := Parse(tc.url)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, %v", tc.url, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPages(t *testing.T) {
	gh := Repo{GitHub, "github.com", "o/r"}
	gl := Repo{GitLab, "gitlab.com", "g/p"}
	for _, tc := range []struct{ got, want string }{
		{gh.BranchPulls("feat/a b"), "https://github.com/o/r/pulls?q=is%3Apr+is%3Aopen+head%3Afeat%2Fa+b"},
		{gh.NewPull("feat/a b"), "https://github.com/o/r/compare/feat/a%20b?expand=1"},
		{gl.BranchPulls("feat/a"), "https://gitlab.com/g/p/-/merge_requests?state=opened&source_branch=feat%2Fa"},
		{gl.NewPull("feat/a"), "https://gitlab.com/g/p/-/merge_requests/new?merge_request%5Bsource_branch%5D=feat%2Fa"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %s, want %s", tc.got, tc.want)
		}
	}
}

func TestParseGitHub(t *testing.T) {
	out := `{"data":{"repository":{
		"b0":{"nodes":[{"number":7,"title":"Add x","url":"https://github.com/o/r/pull/7","isDraft":false,"headRefName":"x",
			"headRepositoryOwner":{"login":"O"},"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}}]},
		"b1":{"nodes":[{"number":8,"title":"From a fork","url":"u","isDraft":false,"headRefName":"y",
			"headRepositoryOwner":{"login":"someone"},"commits":{"nodes":[]}},
			{"number":9,"title":"Draft y","url":"u9","isDraft":true,"headRefName":"y",
			"headRepositoryOwner":{"login":"o"},"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}]},
		"b2":{"nodes":[]}}}}`
	got := map[string]Pull{}
	if err := parseGitHub([]byte(out), "o", 3, got); err != nil {
		t.Fatal(err)
	}
	want := map[string]Pull{
		"x": {Number: 7, Title: "Add x", URL: "https://github.com/o/r/pull/7", Branch: "x", Checks: ChecksFailing},
		"y": {Number: 9, Title: "Draft y", URL: "u9", Branch: "y", Draft: true},
	}
	if len(got) != len(want) || got["x"] != want["x"] || got["y"] != want["y"] {
		t.Fatalf("got %+v", got)
	}
	err := parseGitHub([]byte(`{"errors":[{"message":"Could not resolve to a Repository"}]}`), "o", 1, got)
	if err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Fatalf("err %v", err)
	}
}

func TestParseGitLab(t *testing.T) {
	out := `{"data":{"project":{"mergeRequests":{"nodes":[
		{"iid":"3","title":"Fix","webUrl":"https://gitlab.com/g/p/-/merge_requests/3","draft":false,"sourceBranch":"fix",
			"sourceProject":{"fullPath":"g/p"},"headPipeline":{"status":"RUNNING"}},
		{"iid":"4","title":"Fork","webUrl":"u","draft":false,"sourceBranch":"other",
			"sourceProject":{"fullPath":"me/p"},"headPipeline":null}]}}}}`
	got := map[string]Pull{}
	if err := parseGitLab([]byte(out), "g/p", got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["fix"] != (Pull{Number: 3, Title: "Fix", URL: "https://gitlab.com/g/p/-/merge_requests/3", Branch: "fix", Checks: ChecksPending}) {
		t.Fatalf("got %+v", got)
	}
	if err := parseGitLab([]byte(`{"data":{"project":null}}`), "g/p", got); err == nil {
		t.Fatal("no error for a project not found")
	}
}

func TestQueries(t *testing.T) {
	q := githubQuery(2)
	for _, s := range []string{"$b0:String!", "$b1:String!", "b1:pullRequests(headRefName:$b1"} {
		if !strings.Contains(q, s) {
			t.Errorf("query lacks %s: %s", s, q)
		}
	}
	// Names are written as GraphQL strings, so a quote ends nothing.
	q = gitlabQuery("g/p", []string{`a"b`, `c\d`})
	if !strings.Contains(q, `sourceBranches:["a\"b","c\\d"]`) {
		t.Errorf("query %s", q)
	}
}

// fakeTool writes a gh that prints out and exits with code, and returns
// the environment that finds it.
func fakeTool(t *testing.T, out string, code int) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + out + "\nEOF\n"
	if code != 0 {
		script = "#!/bin/sh\necho \"" + out + "\" >&2\nexit " + string(rune('0'+code)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + ":/usr/bin:/bin"}
}

func TestCLI(t *testing.T) {
	r := Repo{GitHub, "github.com", "o/r"}
	if _, err := Find([]string{"PATH=" + t.TempDir()}, GitHub); !errors.Is(err, ErrNoCLI) {
		t.Fatalf("Find without gh: %v", err)
	}
	c, err := Find(fakeTool(t, `{"data":{"repository":{"b0":{"nodes":[{"number":1,"title":"t","url":"u","headRefName":"x","headRepositoryOwner":{"login":"o"}}]}}}}`, 0), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Pulls(context.Background(), r, []string{"x"})
	if err != nil || got["x"].Number != 1 {
		t.Fatalf("%+v, %v", got, err)
	}

	c, err = Find(fakeTool(t, "To get started with GitHub CLI, please run:  gh auth login", 4), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Pulls(context.Background(), r, []string{"x"})
	var le *LoginError
	if !errors.As(err, &le) || le.Host != "github.com" {
		t.Fatalf("err %v", err)
	}
}

// TestLiveGitHub asks GitHub, through the gh logged in on this machine,
// about a public repository's open pull requests: RAMUS_FORGE_LIVE=1.
func TestLiveGitHub(t *testing.T) {
	if os.Getenv("RAMUS_FORGE_LIVE") == "" {
		t.Skip("RAMUS_FORGE_LIVE is not set")
	}
	out, err := exec.Command("gh", "pr", "list", "--repo", "cli/cli", "--limit", "3", "--json", "headRefName", "--jq", ".[].headRefName").Output()
	if err != nil {
		t.Fatal(err)
	}
	heads := strings.Fields(string(out))
	c, err := Find(os.Environ(), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Pulls(context.Background(), Repo{GitHub, "github.com", "cli/cli"}, append(heads, "no-such-branch-here"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("heads %q: %+v", heads, got)
	if _, ok := got["no-such-branch-here"]; ok {
		t.Fatal("a pull request for a branch that has none")
	}
}
