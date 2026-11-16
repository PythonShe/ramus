package forge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
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
		{gh.BranchPulls("feat/a b"), "https://github.com/o/r/pulls?q=is%3Apr+head%3Afeat%2Fa+b"},
		{gh.NewPull("feat/a b"), "https://github.com/o/r/compare/feat/a%20b?expand=1"},
		{gl.BranchPulls("feat/a"), "https://gitlab.com/g/p/-/merge_requests?state=all&source_branch=feat%2Fa"},
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
		"c0":{"nodes":[{"number":5,"title":"Old x","url":"u5","state":"CLOSED","headRefName":"x","headRepositoryOwner":{"login":"o"}}]},
		"b2":{"nodes":[]},
		"c2":{"nodes":[{"number":4,"title":"Someone's z","url":"u4","state":"MERGED","headRefName":"z","headRepositoryOwner":{"login":"someone"}},
			{"number":3,"title":"Merged z","url":"u3","state":"MERGED","headRefName":"z","headRepositoryOwner":{"login":"o"}},
			{"number":2,"title":"Closed z","url":"u2","state":"CLOSED","headRefName":"z","headRepositoryOwner":{"login":"o"}}]}}}}`
	got := map[string]Pull{}
	if err := parseGitHub([]byte(out), "o", 3, got); err != nil {
		t.Fatal(err)
	}
	// The open one wins over the closed; the latest closed one, of this
	// owner's, over the rest.
	want := map[string]Pull{
		"x": {Number: 7, Title: "Add x", URL: "https://github.com/o/r/pull/7", Branch: "x", Checks: ChecksFailing},
		"y": {Number: 9, Title: "Draft y", URL: "u9", Branch: "y", Draft: true},
		"z": {Number: 3, Title: "Merged z", URL: "u3", Branch: "z", State: PullMerged},
	}
	if len(got) != len(want) || got["x"] != want["x"] || got["y"] != want["y"] || got["z"] != want["z"] {
		t.Fatalf("got %+v", got)
	}
	err := parseGitHub([]byte(`{"errors":[{"message":"Could not resolve to a Repository"}]}`), "o", 1, got)
	if err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Fatalf("err %v", err)
	}
}

func TestParseGitLab(t *testing.T) {
	out := `{"data":{"project":{"o":{"nodes":[
		{"iid":"3","title":"Fix","webUrl":"https://gitlab.com/g/p/-/merge_requests/3","draft":false,"state":"opened","sourceBranch":"fix",
			"sourceProject":{"fullPath":"g/p"},"headPipeline":{"status":"RUNNING"}},
		{"iid":"4","title":"Fork","webUrl":"u","draft":false,"state":"opened","sourceBranch":"other",
			"sourceProject":{"fullPath":"me/p"},"headPipeline":null}]},
		"d":{"nodes":[
		{"iid":"2","title":"Old fix","webUrl":"u2","state":"closed","sourceBranch":"fix","sourceProject":{"fullPath":"g/p"}},
		{"iid":"1","title":"Done","webUrl":"u1","state":"merged","sourceBranch":"done","sourceProject":{"fullPath":"g/p"}}]}}}}`
	got := map[string]Pull{}
	if err := parseGitLab([]byte(out), "g/p", "g/p", got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["fix"] != (Pull{Number: 3, Title: "Fix", URL: "https://gitlab.com/g/p/-/merge_requests/3", Branch: "fix", Checks: ChecksPending}) ||
		got["done"] != (Pull{Number: 1, Title: "Done", URL: "u1", Branch: "done", State: PullMerged}) {
		t.Fatalf("got %+v", got)
	}
	// Asked from the fork me/p, only its merge requests into g/p count.
	got = map[string]Pull{}
	if err := parseGitLab([]byte(out), "g/p", "me/p", got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["other"].Number != 4 {
		t.Fatalf("from the fork: %+v", got)
	}
	if err := parseGitLab([]byte(`{"data":{"project":null}}`), "g/p", "g/p", got); err == nil {
		t.Fatal("no error for a project not found")
	}
}

func TestSquashHint(t *testing.T) {
	for _, tc := range []struct {
		subject string
		want    int
	}{
		{"feat(ui): truncate at the start (#1869)", 1869},
		{"Fix (#12) ", 12},
		{"Fix #12", 0},
		{"Fix (#12) and more", 0},
		{"Fix (#)", 0},
		{"Fix (#+1)", 0},
		{"Fix (#-1)", 0},
		{"Fix (#1x)", 0},
		{"", 0},
	} {
		if got := SquashHint(tc.subject); got != tc.want {
			t.Errorf("SquashHint(%q) = %d, want %d", tc.subject, got, tc.want)
		}
	}
}

func TestPickCommitPull(t *testing.T) {
	merged := "2026-01-01T00:00:00Z"
	var list []Pull
	for _, rp := range []restPull{
		{Number: 1, State: "open"},
		{Number: 2, State: "closed", MergedAt: &merged},
		{Number: 3, State: "closed"},
	} {
		list = append(list, rp.pull())
	}
	if p, ok := pickCommitPull(list, 0); !ok || p.Number != 2 || p.State != PullMerged {
		t.Fatalf("merged first: %+v %v", p, ok)
	}
	if p, ok := pickCommitPull(list, 3); !ok || p.Number != 3 || p.State != PullClosed {
		t.Fatalf("the hint first: %+v %v", p, ok)
	}
	if p, ok := pickCommitPull(list[:1], 0); !ok || p.Number != 1 || p.State != PullOpen {
		t.Fatalf("open: %+v %v", p, ok)
	}
	if _, ok := pickCommitPull(nil, 3); ok {
		t.Fatal("found in none")
	}
}

func TestQueries(t *testing.T) {
	q := githubQuery(2)
	for _, s := range []string{"$b0:String!", "$b1:String!", "b1:pullRequests(headRefName:$b1,states:[OPEN]",
		"c1:pullRequests(headRefName:$b1,states:[MERGED,CLOSED]"} {
		if !strings.Contains(q, s) {
			t.Errorf("query lacks %s: %s", s, q)
		}
	}
	// Names are written as GraphQL strings, so a quote ends nothing.
	q = gitlabQuery("g/p", []string{`a"b`, `c\d`})
	if strings.Count(q, `sourceBranches:["a\"b","c\\d"]`) != 2 {
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
	got, err := c.Pulls(context.Background(), r, r, []string{"x"})
	// The tool's "u" is no page: the one built from the number stands.
	if err != nil || got["x"].Number != 1 || got["x"].URL != "https://github.com/o/r/pull/1" {
		t.Fatalf("%+v, %v", got, err)
	}
	// Asked for a fork's branches, the same answer names none of them.
	got, err = c.Pulls(context.Background(), r, Repo{GitHub, "github.com", "me/r"}, []string{"x"})
	if err != nil || len(got) != 0 {
		t.Fatalf("from a fork: %+v, %v", got, err)
	}

	c, err = Find(fakeTool(t, "To get started with GitHub CLI, please run:  gh auth login", 4), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Pulls(context.Background(), r, r, []string{"x"})
	var le *LoginError
	if !errors.As(err, &le) || le.Host != "github.com" {
		t.Fatalf("err %v", err)
	}
}

// fakeREST writes a gh that answers each REST path it is given by the
// case script, and returns the environment that finds it.
func fakeREST(t *testing.T, cases string) []string {
	t.Helper()
	return fakeRESTTool(t, "gh", cases)
}

// fakeRESTTool is fakeREST for the program named program.
func fakeRESTTool(t *testing.T, program, cases string) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$2\" in\n" + cases + "\n*) echo \"" + program + ": 404 Not Found (HTTP 404)\" >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, program), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + ":/usr/bin:/bin"}
}

func TestCommitPull(t *testing.T) {
	r := Repo{GitHub, "github.com", "up/r"}
	sha := strings.Repeat("ab", 20)
	other := strings.Repeat("cd", 20)
	c, err := Find(fakeREST(t, `repos/up/r/commits/`+sha+`/pulls) echo '[{"number":1869,"title":"Truncate","html_url":"https://github.com/up/r/pull/1869","state":"closed","merged_at":"2026-11-01T00:00:00Z","head":{"ref":"truncate-start"}}]' ;;
repos/up/r/commits/`+other+`/pulls) echo "gh: No commit found for SHA: x (HTTP 422)" >&2; exit 1 ;;
repos/up/r/pulls/12) echo '{"number":12,"title":"Twelve","html_url":"u12","state":"open","merged_at":null}' ;;`), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, ok, err := c.CommitPull(ctx, r, sha, 0)
	if err != nil || !ok || p != (Pull{Number: 1869, Title: "Truncate", URL: "https://github.com/up/r/pull/1869", Branch: "truncate-start", State: PullMerged}) {
		t.Fatalf("%+v %v %v", p, ok, err)
	}
	// A commit GitHub has not seen: the subject's number is asked for.
	p, ok, err = c.CommitPull(ctx, r, other, 12)
	if err != nil || !ok || p.Number != 12 || p.State != PullOpen {
		t.Fatalf("by the hint: %+v %v %v", p, ok, err)
	}
	if _, ok, err := c.CommitPull(ctx, r, other, 0); err != nil || ok {
		t.Fatalf("without a hint: %v %v", ok, err)
	}
	if _, ok, err := c.CommitPull(ctx, r, other, 13); err != nil || ok {
		t.Fatalf("a hint not found: %v %v", ok, err)
	}
	if _, _, err := c.CommitPull(ctx, r, "../x", 0); err == nil {
		t.Fatal("a path taken for a commit id")
	}
}

// TestLiveGitHub asks GitHub, through the gh logged in on this machine,
// about a public repository's pull requests and the pull request a
// commit came in by: RAMUS_FORGE_LIVE=1.
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
	cli := Repo{GitHub, "github.com", "cli/cli"}
	got, err := c.Pulls(context.Background(), cli, cli, append(heads, "no-such-branch-here"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("heads %q: %+v", heads, got)
	if _, ok := got["no-such-branch-here"]; ok {
		t.Fatal("a pull request for a branch that has none")
	}

	// The pull request a merged one's commit came in by.
	out, err = exec.Command("gh", "pr", "list", "--repo", "cli/cli", "--state", "merged", "--limit", "1", "--json", "number,mergeCommit",
		"--jq", `.[0] | "\(.number) \(.mergeCommit.oid)"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		t.Fatalf("gh pr list: %q", out)
	}
	p, ok, err := c.CommitPull(context.Background(), cli, f[1], 0)
	if err != nil || !ok || strconv.Itoa(p.Number) != f[0] || p.State != PullMerged {
		t.Fatalf("commit %s of #%s: %+v %v %v", f[1], f[0], p, ok, err)
	}
}

func TestStacks(t *testing.T) {
	dir := t.TempDir()
	if st, err := ReadStacks(dir); err != nil || st != nil {
		t.Fatalf("no file: %v %v", st, err)
	}
	file := `{"schemaVersion":1,"repository":"github.com/o/r","stacks":[
		{"id":"S_1","number":3,"trunk":{"branch":"main","head":"aa"},"branches":[
			{"branch":"auth","head":"bb","pullRequest":{"number":41,"id":"PR_1","url":"https://github.com/o/r/pull/41","merged":true}},
			{"branch":"api"}]},
		{"trunk":{"branch":"main"},"branches":[]}]}`
	if err := os.WriteFile(filepath.Join(dir, "gh-stack"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := ReadStacks(dir)
	if err != nil || len(st) != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	want := Stack{Number: 3, Trunk: "main", Branches: []StackBranch{{Name: "auth", PR: 41, URL: "https://github.com/o/r/pull/41", Merged: true}, {Name: "api"}}}
	if st[0].Number != want.Number || st[0].Trunk != want.Trunk || len(st[0].Branches) != 2 || st[0].Branches[0] != want.Branches[0] || st[0].Branches[1] != want.Branches[1] {
		t.Fatalf("got %+v", st[0])
	}
	if s, i, ok := StackOf(st, "api"); !ok || i != 1 || s.Number != 3 {
		t.Fatalf("StackOf api: %v %d %v", s, i, ok)
	}
	if _, _, ok := StackOf(st, "main"); ok {
		t.Fatal("the trunk is in its stack")
	}
	if got := lastFailure("Pushing...\n✗ failed to push: rejected\nhint: x\n"); got != "failed to push: rejected" {
		t.Fatalf("lastFailure %q", got)
	}
}

// A pull request's URL, as the tool answers it, is opened only when it is
// a web page on the host asked; else it is built from the number.
func TestPullURL(t *testing.T) {
	gh := Repo{GitHub, "github.com", "o/r"}
	gl := Repo{GitLab, "gitlab.com", "g/p"}
	self := Repo{GitLab, "gitlab.corp.example", "g/p"}
	for _, tc := range []struct {
		r     Repo
		given string
		n     int
		want  string
	}{
		{gh, "https://github.com/o/r/pull/7", 7, "https://github.com/o/r/pull/7"},
		{gh, "https://GitHub.com/o/r/pull/7", 7, "https://GitHub.com/o/r/pull/7"},
		{gh, "http://github.com/o/r/pull/7", 7, "https://github.com/o/r/pull/7"},
		{gh, "https://github.com.evil.example/o/r/pull/7", 7, "https://github.com/o/r/pull/7"},
		{gh, "https://github.com@evil.example/pull/7", 7, "https://github.com/o/r/pull/7"},
		{gh, "https://me@github.com/o/r/pull/7", 7, "https://github.com/o/r/pull/7"},
		{gh, "javascript:alert(1)", 7, "https://github.com/o/r/pull/7"},
		{gh, "file:///etc/passwd", 7, "https://github.com/o/r/pull/7"},
		{gh, "x-github-client://open", 0, ""},
		{gh, "", 3, "https://github.com/o/r/pull/3"},
		{gl, "https://gitlab.com/g/p/-/merge_requests/3", 3, "https://gitlab.com/g/p/-/merge_requests/3"},
		{gl, "https://evil.example/g/p/-/merge_requests/3", 3, "https://gitlab.com/g/p/-/merge_requests/3"},
		{self, "http://gitlab.corp.example/g/p/-/merge_requests/3", 3, "http://gitlab.corp.example/g/p/-/merge_requests/3"},
		{self, "http://gitlab.corp.example:8080/g/p/-/merge_requests/3", 3, "http://gitlab.corp.example:8080/g/p/-/merge_requests/3"},
		{self, "http://other.example/g/p/-/merge_requests/3", 3, "https://gitlab.corp.example/g/p/-/merge_requests/3"},
	} {
		if got := tc.r.PullURL(tc.given, tc.n); got != tc.want {
			t.Errorf("PullURL(%q, %d) on %s = %q, want %q", tc.given, tc.n, tc.r.Host, got, tc.want)
		}
	}
}

// Knows takes the public host, the one the environment names, and those
// the tool's own config is logged in to, read again when it changes; a
// remote naming any other github.* or gitlab.* host is not asked.
func TestKnownHosts(t *testing.T) {
	conf := t.TempDir()
	c, err := Find(append(fakeTool(t, `{}`, 0), "GH_CONFIG_DIR="+conf, "GH_HOST=ghe.corp.example"), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Knows("github.com") || !c.Knows("ghe.corp.example") || c.Knows("github.corp.example") || c.Knows("gitlab.com") {
		t.Fatal("without a config")
	}
	hosts := "github.com:\n    user: me\ngithub.corp.example:\n    oauth_token: x\n    users:\n        me:\n"
	if err := os.WriteFile(filepath.Join(conf, "hosts.yml"), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	if !c.Knows("GitHub.Corp.Example") || c.Knows("github.evil.example") || c.Knows("me") || c.Knows("users") {
		t.Fatalf("with %q", hosts)
	}
	spoofed := Repo{GitHub, "github.evil.example", "o/r"}
	if _, err := c.Pulls(context.Background(), spoofed, spoofed, []string{"x"}); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("pulls on a spoofed host: %v", err)
	}
	if _, _, err := c.CommitPull(context.Background(), spoofed, strings.Repeat("ab", 20), 0); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("commit pull on a spoofed host: %v", err)
	}

	glab := "git_protocol: ssh\nhosts:\n  gitlab.com:\n    token: x\n    api_host: gitlab.com\n  gitlab.corp.example:8443:\n    token: y\n    nested:\n      deeper.example:\nother:\n  gitlab.evil.example:\n"
	got := configHosts(GitLab, []byte(glab))
	if strings.Join(got, " ") != "gitlab.com gitlab.corp.example:8443" {
		t.Fatalf("glab hosts %q", got)
	}
	if got := configHosts(GitHub, []byte(hosts)); strings.Join(got, " ") != "github.com github.corp.example" {
		t.Fatalf("gh hosts %q", got)
	}
}

// A fork names its parent; a repository that is none, or names one no
// remote could, has none.
func TestParent(t *testing.T) {
	c, err := Find(fakeREST(t, `repos/me/r) echo '{"full_name":"me/r","fork":true,"parent":{"full_name":"up/r"}}' ;;
repos/up/r) echo '{"full_name":"up/r","fork":false}' ;;
repos/me/odd) echo '{"parent":{"full_name":"../x"}}' ;;`), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, ok, err := c.Parent(ctx, Repo{GitHub, "github.com", "me/r"})
	if err != nil || !ok || p != (Repo{GitHub, "github.com", "up/r"}) {
		t.Fatalf("%+v %v %v", p, ok, err)
	}
	for _, path := range []string{"up/r", "me/odd"} {
		if p, ok, err := c.Parent(ctx, Repo{GitHub, "github.com", path}); err != nil || ok {
			t.Errorf("%s: %+v %v %v", path, p, ok, err)
		}
	}
	if _, _, err := c.Parent(ctx, Repo{GitHub, "github.evil.example", "me/r"}); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("an unknown host: %v", err)
	}
}

// GitLab names the merge requests a commit is in, merged first; one it
// has not seen is in none. A fork names the project it was forked from.
func TestGitLabCommitPull(t *testing.T) {
	r := Repo{GitLab, "gitlab.com", "g/sub/p"}
	sha := strings.Repeat("ab", 20)
	c, err := Find(fakeRESTTool(t, "glab", `projects/g%2Fsub%2Fp/repository/commits/`+sha+`/merge_requests) echo '[{"iid":4,"title":"Open","web_url":"https://gitlab.com/g/sub/p/-/merge_requests/4","state":"opened","source_branch":"x"},{"iid":3,"title":"Merged","web_url":"https://gitlab.com/g/sub/p/-/merge_requests/3","state":"merged","source_branch":"y"}]' ;;
projects/me%2Fp) echo '{"forked_from_project":{"path_with_namespace":"g/sub/p"}}' ;;`), GitLab)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, ok, err := c.CommitPull(ctx, r, sha, 0)
	if err != nil || !ok || p != (Pull{Number: 3, Title: "Merged", URL: "https://gitlab.com/g/sub/p/-/merge_requests/3", Branch: "y", State: PullMerged}) {
		t.Fatalf("%+v %v %v", p, ok, err)
	}
	if _, ok, err := c.CommitPull(ctx, r, strings.Repeat("cd", 20), 0); err != nil || ok {
		t.Fatalf("a commit not seen: %v %v", ok, err)
	}
	if p, ok, err := c.Parent(ctx, Repo{GitLab, "gitlab.com", "me/p"}); err != nil || !ok || p != r {
		t.Fatalf("parent %+v %v %v", p, ok, err)
	}
}

// A refusal keeps what the host said; only 404 is not found.
func TestFailure(t *testing.T) {
	c := &CLI{Kind: GitHub}
	fail := errors.New("exit status 1")
	err := c.failure(fail, "github.com", "gh: Validation Failed (HTTP 422)\nPull request review thread line must be part of the diff\n")
	var rej *RejectedError
	if !errors.As(err, &rej) || errors.Is(err, ErrNotFound) ||
		rej.Msg != "Validation Failed (HTTP 422) Pull request review thread line must be part of the diff" {
		t.Fatalf("422: %#v", err)
	}
	if err := c.failure(fail, "github.com", "gh: Not Found (HTTP 404)\n"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	if err := c.failure(fail, "github.com", "gh: "+strings.Repeat("é", 400)+" (HTTP 422)"); !errors.As(err, &rej) || len(rej.Msg) > 503 || !utf8.ValidString(rej.Msg) {
		t.Fatalf("long 422: %d bytes", len(rej.Msg))
	}
}
