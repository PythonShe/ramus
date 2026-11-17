package write

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/run"
)

// The identity goes into the global config as typed, a name starting
// with a dash among it, and a field emptied comes out; a default branch
// git would refuse is refused before anything is written.
func TestSetIdentity(t *testing.T) {
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[core]\n\tautocrlf = input\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"GIT_CONFIG_GLOBAL=" + cfg}
	for _, kv := range gittest.Env() {
		if !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
			env = append(env, kv)
		}
	}
	r := run.New(g, env, 2)
	ctx := context.Background()
	was, err := ReadIdentity(ctx, r)
	if err != nil || was != (Identity{}) {
		t.Fatalf("read at first: %+v %v", was, err)
	}
	now := Identity{Name: "-x Someone", Email: "some@example.com", DefaultBranch: "trunk"}
	if err := SetIdentity(ctx, r, was, now); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadIdentity(ctx, r); err != nil || got != now {
		t.Fatalf("read %+v %v, want %+v", got, err, now)
	}
	later := Identity{Name: "Someone", Email: "some@example.com"}
	if err := SetIdentity(ctx, r, now, later); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadIdentity(ctx, r); err != nil || got != later {
		t.Fatalf("read %+v %v, want %+v", got, err, later)
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "autocrlf = input") {
		t.Fatalf("the rest of the config went:\n%s", b)
	}
	for _, bad := range []string{"two words", "-b", "a..b", "x.lock"} {
		if err := SetIdentity(ctx, r, later, Identity{Name: "Changed", DefaultBranch: bad}); !errors.Is(err, ErrBranchName) {
			t.Errorf("default branch %q: %v", bad, err)
		}
	}
	if got, _ := ReadIdentity(ctx, r); got != later {
		t.Fatalf("a refused identity was written: %+v", got)
	}
}
