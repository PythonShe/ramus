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

func TestPeopleQuery(t *testing.T) {
	q := peopleQuery([]string{strings.Repeat("a", 40), strings.Repeat("b", 40)})
	for _, want := range []string{
		`c0:object(oid:"` + strings.Repeat("a", 40) + `"){...p}`,
		`c1:object(oid:"` + strings.Repeat("b", 40) + `"){...p}`,
		"avatarUrl(size:64)", "authors(first:10)", "committer{email user{...u}}",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %s:\n%s", want, q)
		}
	}
}

func TestParsePeople(t *testing.T) {
	gh := Repo{GitHub, "github.com", "o/r"}
	shas := []string{"a1", "b2", "c3", "d4"}
	// c0: an author, a co-author without an account, the committer GitHub
	// itself, and the author again as the committer; c1: a commit GitHub
	// lacks; c2: a picture elsewhere than GitHub's; c3: not a commit.
	ans := `{"data":{"repository":{
		"c0":{"authors":{"nodes":[
			{"email":"Ann@Example.com","user":{"login":"ann","avatarUrl":"https://avatars.githubusercontent.com/u/1?s=64&v=4"}},
			{"email":"bob@example.com","user":null}]},
			"committer":{"email":"ann@example.com","user":{"login":"ann","avatarUrl":"https://avatars.githubusercontent.com/u/1?s=64&v=4"}}},
		"c1":null,
		"c2":{"authors":{"nodes":[{"email":"eve@example.com","user":{"login":"eve","avatarUrl":"https://evil.example/u/2"}}]},"committer":{"email":"","user":null}},
		"c3":{}}}}`
	out := map[string][]Person{}
	if err := parsePeople([]byte(ans), gh, shas, out); err != nil {
		t.Fatal(err)
	}
	want := []Person{
		{Email: "ann@example.com", Login: "ann", Avatar: "https://avatars.githubusercontent.com/u/1?s=64&v=4"},
		{Email: "bob@example.com"},
	}
	if got := out["a1"]; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("a1: %+v", got)
	}
	if _, ok := out["b2"]; ok {
		t.Error("a commit GitHub lacks has people")
	}
	if got := out["c3"]; len(got) != 1 || got[0] != (Person{Email: "eve@example.com"}) {
		t.Errorf("c3: %+v", got)
	}
	if _, ok := out["d4"]; ok {
		t.Error("a tree has people")
	}

	err := parsePeople([]byte(`{"data":null,"errors":[{"message":"API rate limit exceeded"}]}`), gh, shas, out)
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("rate limited: %v", err)
	}
}

func TestAvatarURL(t *testing.T) {
	gh := Repo{GitHub, "github.com", "o/r"}
	ghe := Repo{GitHub, "github.corp.example", "o/r"}
	for _, tc := range []struct {
		r    Repo
		url  string
		want bool
	}{
		{gh, "https://avatars.githubusercontent.com/u/1?s=64&v=4", true},
		{gh, "http://avatars.githubusercontent.com/u/1", false},
		{gh, "https://avatars.githubusercontent.com:8443/u/1", false},
		{gh, "https://user@avatars.githubusercontent.com/u/1", false},
		{gh, "https://github.com/u/1", false},
		{gh, "https://evil.example/u/1", false},
		{gh, "-X", false},
		{gh, "avatars.githubusercontent.com/u/1", false},
		{ghe, "https://github.corp.example/avatars/u/1?s=64", true},
		{ghe, "http://avatars.github.corp.example/u/1", true},
		{ghe, "https://avatars.githubusercontent.com/u/1", false},
		{Repo{GitLab, "gitlab.com", "o/r"}, "https://gitlab.com/uploads/a.png", false},
	} {
		if got := tc.r.avatarURL(tc.url); got != tc.want {
			t.Errorf("%s on %s: %v", tc.url, tc.r.Host, got)
		}
	}
}

// pictureTool writes a gh that prints data for any call, and returns the
// environment that finds it and the arguments it last ran with.
func pictureTool(t *testing.T, data []byte) (env []string, args func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	dir := t.TempDir()
	pic := filepath.Join(dir, "pic")
	if err := os.WriteFile(pic, data, 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" > '" + filepath.Join(dir, "args") + "'\ncat '" + pic + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	args = func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "args"))
		return strings.TrimSpace(string(b))
	}
	return []string{"PATH=" + dir + ":/usr/bin:/bin", "GH_CONFIG_DIR=" + t.TempDir()}, args
}

func TestAvatar(t *testing.T) {
	gh := Repo{GitHub, "github.com", "o/r"}
	addr := "https://avatars.githubusercontent.com/u/1?s=64&v=4"
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...)
	env, args := pictureTool(t, png)
	c, err := Find(env, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Avatar(context.Background(), gh, addr)
	if err != nil || string(got) != string(png) {
		t.Fatalf("%d bytes, %v", len(got), err)
	}
	if a := args(); a != "api "+addr {
		t.Errorf("ran gh %s", a)
	}
	if _, err := c.Avatar(context.Background(), gh, "https://evil.example/u/1"); err == nil {
		t.Error("downloaded a picture off GitHub's picture host")
	}
	if _, err := c.Avatar(context.Background(), Repo{GitHub, "github.unknown.example", "o/r"}, "https://github.unknown.example/avatars/u/1"); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("unknown host: %v", err)
	}

	env, _ = pictureTool(t, []byte("<html>not a picture</html>"))
	c, err = Find(env, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Avatar(context.Background(), gh, addr); !errors.Is(err, ErrNotAPicture) {
		t.Errorf("html: %v", err)
	}

	env, _ = pictureTool(t, append(png, make([]byte, MaxAvatar)...))
	c, err = Find(env, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Avatar(context.Background(), gh, addr); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
}

func TestCommitPeople(t *testing.T) {
	gh := Repo{GitHub, "github.com", "o/r"}
	sha := strings.Repeat("ab", 20)
	c, err := Find(fakeTool(t, `{"data":{"repository":{"c0":{"authors":{"nodes":[{"email":"a@x","user":{"login":"a","avatarUrl":"https://avatars.githubusercontent.com/u/1"}}]},"committer":{"email":"a@x","user":null}}}}}`, 0), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.CommitPeople(context.Background(), gh, []string{sha})
	if err != nil || len(got[sha]) != 1 || got[sha][0].Login != "a" {
		t.Fatalf("%+v, %v", got, err)
	}
	if _, err := c.CommitPeople(context.Background(), gh, []string{"HEAD"}); err == nil {
		t.Error("asked about a name that is no commit id")
	}
	if _, err := c.CommitPeople(context.Background(), Repo{GitHub, "github.evil.example", "o/r"}, []string{sha}); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("unknown host: %v", err)
	}
}

func TestLiveAvatars(t *testing.T) {
	if os.Getenv("RAMUS_FORGE_LIVE") == "" {
		t.Skip("RAMUS_FORGE_LIVE is not set")
	}
	out, err := exec.Command("gh", "api", "repos/cli/cli/commits?per_page=3", "--jq", ".[].sha").Output()
	if err != nil {
		t.Fatal(err)
	}
	shas := strings.Fields(string(out))
	c, err := Find(os.Environ(), GitHub)
	if err != nil {
		t.Fatal(err)
	}
	cli := Repo{GitHub, "github.com", "cli/cli"}
	got, err := c.CommitPeople(context.Background(), cli, append(shas, strings.Repeat("0", 40)))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", got)
	for _, sha := range shas {
		for _, p := range got[sha] {
			if p.Avatar == "" {
				continue
			}
			b, err := c.Avatar(context.Background(), cli, p.Avatar)
			if err != nil {
				t.Fatalf("%s: %v", p.Avatar, err)
			}
			t.Logf("%s: %d bytes", p.Login, len(b))
			return
		}
	}
	t.Fatal("no picture among the people of the newest commits")
}
