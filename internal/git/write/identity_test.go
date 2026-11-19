package write

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// identityConfig makes a folder of config files, name to content, the
// one named "gitconfig" git's global config, and a runner that reads it.
// "{dir}" in a content is the folder.
func identityConfig(t *testing.T, files map[string]string) (*run.Runner, string) {
	t.Helper()
	g := testrun.Git(t)
	dir := t.TempDir()
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d
	}
	for name, content := range files {
		content = strings.ReplaceAll(content, "{dir}", filepath.ToSlash(dir))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"GIT_CONFIG_GLOBAL=" + filepath.Join(dir, "gitconfig")}
	for _, kv := range gittest.Env() {
		if !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
			env = append(env, kv)
		}
	}
	return run.New(g, env, 2), dir
}

func readIdentityT(t *testing.T, r *run.Runner, w Where) (Identity, IdentityFrom) {
	t.Helper()
	id, from, err := ReadIdentity(context.Background(), r, w)
	if err != nil {
		t.Fatal(err)
	}
	return id, from
}

// The identity goes into the global config as typed, a name starting
// with a dash among it, and a field emptied comes out; a default branch
// git would refuse is refused before anything is written.
func TestSetIdentity(t *testing.T) {
	r, dir := identityConfig(t, map[string]string{"gitconfig": "[core]\n\tautocrlf = input\n"})
	cfg := filepath.Join(dir, "gitconfig")
	ctx := context.Background()
	was, from := readIdentityT(t, r, Where{})
	if was != (Identity{}) || from[0].File != "" || from[0].Kept != "" {
		t.Fatalf("read at first: %+v %+v", was, from)
	}
	now := Identity{Name: "-x Someone", Email: "some@example.com", DefaultBranch: "trunk"}
	if _, err := SetIdentity(ctx, r, was, now, from); err != nil {
		t.Fatal(err)
	}
	got, from := readIdentityT(t, r, Where{})
	if got != now {
		t.Fatalf("read %+v, want %+v", got, now)
	}
	if o := from[0]; o.File != cfg || o.Included || o.Kept != "" {
		t.Fatalf("the name comes from %+v, want the global config", o)
	}
	later := Identity{Name: "Someone", Email: "some@example.com"}
	if _, err := SetIdentity(ctx, r, now, later, from); err != nil {
		t.Fatal(err)
	}
	if got, _ := readIdentityT(t, r, Where{}); got != later {
		t.Fatalf("read %+v, want %+v", got, later)
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "autocrlf = input") {
		t.Fatalf("the rest of the config went:\n%s", b)
	}
	for _, bad := range []string{"two words", "-b", "a..b", "x.lock"} {
		if _, err := SetIdentity(ctx, r, later, Identity{Name: "Changed", DefaultBranch: bad}, from); !errors.Is(err, ErrBranchName) {
			t.Errorf("default branch %q: %v", bad, err)
		}
	}
	if got, _ := readIdentityT(t, r, Where{}); got != later {
		t.Fatalf("a refused identity was written: %+v", got)
	}
}

// An identity kept in a file the global config includes is read, and
// written there: the global config sets the email before the include
// that sets it too, so a change written to the global config itself
// would change nothing git uses. Cleared, a name the global config and
// the included file both set is gone from both, not the included one
// left to come through.
func TestIdentityIncluded(t *testing.T) {
	r, dir := identityConfig(t, map[string]string{
		"gitconfig":       "[user]\n\tname = Plain\n\temail = plain@example.com\n[include]\n\tpath = gitconfig.local\n",
		"gitconfig.local": "[user]\n\tname = Kept Apart\n\temail = apart@example.com\n",
	})
	local := filepath.Join(dir, "gitconfig.local")
	ctx := context.Background()
	was, from := readIdentityT(t, r, Where{})
	if was != (Identity{Name: "Kept Apart", Email: "apart@example.com"}) {
		t.Fatalf("read %+v", was)
	}
	if o := from[1]; o.File != local || !o.Included || len(o.Files) != 2 {
		t.Fatalf("the email comes from %+v, want the included file", o)
	}
	now := Identity{Name: "Kept Apart", Email: "new@example.com"}
	if _, err := SetIdentity(ctx, r, was, now, from); err != nil {
		t.Fatal(err)
	}
	got, from := readIdentityT(t, r, Where{})
	if got != now {
		t.Fatalf("read %+v after the change, want %+v", got, now)
	}
	if b, _ := os.ReadFile(local); !strings.Contains(string(b), "new@example.com") {
		t.Fatalf("the included file has not the change:\n%s", b)
	}
	cleared := Identity{Email: "new@example.com"}
	if _, err := SetIdentity(ctx, r, now, cleared, from); err != nil {
		t.Fatal(err)
	}
	if got, _ := readIdentityT(t, r, Where{}); got != cleared {
		t.Fatalf("read %+v after clearing the name, want %+v", got, cleared)
	}
}

// In a repository, a file the global config includes for it alone
// (includeIf gitdir) counts and is written; out of it, it does not.
// The include names the file in another form than the one paths are
// compared in, as forward slashes are on Windows, and it is still the
// file said, in that one form.
func TestIdentityIncludedForRepository(t *testing.T) {
	repo := gittest.Init(t)
	r, dir := identityConfig(t, map[string]string{
		"gitconfig": "[user]\n\tname = Everywhere\n\temail = home@example.com\n" +
			"[includeIf \"gitdir:" + filepath.ToSlash(repo.Dir) + "/\"]\n\tpath = {dir}/.//work\n",
		"work": "[user]\n\temail = work@example.com\n",
	})
	w := Where{GitDir: repo.GitDir(), WorkTree: repo.Dir}
	was, from := readIdentityT(t, r, w)
	if was != (Identity{Name: "Everywhere", Email: "work@example.com"}) {
		t.Fatalf("read in the repository %+v", was)
	}
	if o := from[1]; o.File != filepath.Join(dir, "work") || !o.Included {
		t.Fatalf("the email comes from %+v, want the conditional file", o)
	}
	if o := from[0]; o.File != filepath.Join(dir, "gitconfig") || o.Included {
		t.Fatalf("the name comes from %+v, want the global config", o)
	}
	now := Identity{Name: "Everywhere", Email: "job@example.com"}
	if _, err := SetIdentity(context.Background(), r, was, now, from); err != nil {
		t.Fatal(err)
	}
	if got, _ := readIdentityT(t, r, w); got != now {
		t.Fatalf("read in the repository %+v, want %+v", got, now)
	}
	if got, _ := readIdentityT(t, r, Where{}); got != (Identity{Name: "Everywhere", Email: "home@example.com"}) {
		t.Fatalf("read out of the repository %+v", got)
	}
}

// A name the repository's own config sets is read as git uses it there,
// and named as kept apart; a change goes to the global config and the
// repository's is left as it is.
func TestIdentityRepositoryLocal(t *testing.T) {
	repo := gittest.Init(t)
	repo.Git("config", "user.name", "Only Here")
	r, dir := identityConfig(t, map[string]string{"gitconfig": "[user]\n\tname = Everywhere\n"})
	w := Where{GitDir: repo.GitDir(), WorkTree: repo.Dir}
	was, from := readIdentityT(t, r, w)
	if was.Name != "Only Here" {
		t.Fatalf("read in the repository %+v", was)
	}
	if o := from[0]; o.Kept != filepath.Join(repo.GitDir(), "config") || o.File != filepath.Join(dir, "gitconfig") {
		t.Fatalf("the name comes from %+v", o)
	}
	now := was
	now.Name = "Changed"
	if _, err := SetIdentity(context.Background(), r, was, now, from); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(repo.Git("config", "--local", "user.name")); got != "Only Here" {
		t.Fatalf("the repository's config was written: %q", got)
	}
	if got, _ := readIdentityT(t, r, Where{}); got.Name != "Changed" {
		t.Fatalf("the global config has %+v", got)
	}
}

// A write that fails part way says what it did write: the name went in,
// the email, in a file git cannot write, did not.
func TestSetIdentityPartly(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a folder the user cannot write to is needed")
	}
	r, dir := identityConfig(t, map[string]string{
		"gitconfig": "[user]\n\tname = Before\n[include]\n\tpath = {dir}/shut/inc\n",
	})
	shut := filepath.Join(dir, "shut")
	if err := os.Mkdir(shut, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shut, "inc"), []byte("[user]\n\temail = before@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// git writes a config through a lock file beside it.
	if err := os.Chmod(shut, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shut, 0o755) })
	was, from := readIdentityT(t, r, Where{})
	got, err := SetIdentity(context.Background(), r, was, Identity{Name: "After", Email: "after@example.com"}, from)
	if err == nil {
		t.Fatal("the email was written")
	}
	if want := (Identity{Name: "After", Email: "before@example.com"}); got != want {
		t.Fatalf("said %+v written, want %+v", got, want)
	}
	if now, _ := readIdentityT(t, r, Where{}); now != got {
		t.Fatalf("the config has %+v, said %+v", now, got)
	}
}
