package write

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
)

// The tool goes into the global config and comes out again, leaving a
// difftool the user chose since, and what else the file holds.
func TestSetTool(t *testing.T) {
	g := testrun.Git(t)
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Someone\n"), 0o644); err != nil {
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
	tool := GlobalTool{Name: "ramus", DiffCmd: `'/opt/My Apps/ramus' diff --wait "$LOCAL" "$REMOTE"`, MergeCmd: `ramus merge "$MERGED"`}
	if on, err := ToolIsSet(ctx, r, tool); err != nil || on {
		t.Fatalf("set at first: %v %v", on, err)
	}
	if err := SetTool(ctx, r, tool, true); err != nil {
		t.Fatal(err)
	}
	if on, err := ToolIsSet(ctx, r, tool); err != nil || !on {
		t.Fatalf("not set: %v %v", on, err)
	}
	if v, _ := globalGet(ctx, r, "difftool.ramus.cmd"); v != tool.DiffCmd {
		t.Fatalf("difftool cmd %q", v)
	}
	// Left by the app at another path, it runs nothing: not set.
	moved := tool
	moved.DiffCmd = `/old/ramus diff --wait "$LOCAL" "$REMOTE"`
	if on, err := ToolIsSet(ctx, r, moved); err != nil || on {
		t.Fatalf("set for another path: %v %v", on, err)
	}
	// Set twice, it is there once.
	if err := SetTool(ctx, r, tool, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); strings.Count(string(b), "[difftool \"ramus\"]") != 1 {
		t.Fatalf("config:\n%s", b)
	}
	if err := global(ctx, r, "config", "--global", "diff.tool", "meld"); err != nil {
		t.Fatal(err)
	}
	if err := SetTool(ctx, r, tool, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfg)
	got := string(b)
	if strings.Contains(got, "ramus") || !strings.Contains(got, "tool = meld") || !strings.Contains(got, "name = Someone") {
		t.Fatalf("config after:\n%s", got)
	}
	// Off again, with nothing to take out, is no error.
	if err := SetTool(ctx, r, tool, false); err != nil {
		t.Fatal(err)
	}
}
