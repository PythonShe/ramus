package ui

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The folder follows the URL until the user types one, Enter in a field
// starts the clone, and the dialog stays, with its progress named, while
// the clone runs: Escape would leave it running with nothing to show it.
func TestCloneDialog(t *testing.T) {
	h := newHarness(t)
	src := h.repoWith()
	parent := t.TempDir()
	h.app.clone.parent, h.app.clone.looked = parent, true
	h.app.ShowClone()
	h.tt.Frame()
	if h.app.clone.dir != parent {
		t.Fatalf("folder before a URL: %q", h.app.clone.dir)
	}
	h.tt.Type("file://" + src.Dir)
	h.tt.Frame()
	h.tt.Frame()
	want := filepath.Join(parent, filepath.Base(src.Dir))
	if h.app.clone.dir != want {
		t.Fatalf("folder %q, want %q", h.app.clone.dir, want)
	}
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Frame()
	if !h.app.clone.starting && h.app.clone.job == nil {
		t.Fatal("Enter did not start the clone")
	}
	named := false
	for _, n := range h.tt.AccessAll() {
		if n.Role == ui.RoleProgress && n.Name == "Cloning" {
			named = true
		}
	}
	if !named {
		t.Error("the clone's progress bar has no name")
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if !h.app.clone.open {
		t.Fatal("Escape hid the clone running")
	}
	h.until("the clone to open", func() bool { return len(h.app.tabs) == 1 && h.tt.HasText("No local changes") })
	if h.app.clone.open {
		t.Fatal("the dialog stayed after the clone")
	}
	if h.app.Settings.CloneParent != parent {
		t.Fatalf("the folder kept for the next clone is %q, want %q", h.app.Settings.CloneParent, parent)
	}
}

// The folder the last clone went into is offered again after a restart,
// while it is there.
func TestCloneParentKept(t *testing.T) {
	h := newHarness(t)
	parent := t.TempDir()
	h.app.Settings.CloneParent = parent
	h.app.ShowClone()
	h.until("the folder", func() bool { return h.app.clone.parent != "" })
	if h.app.clone.parent != parent {
		t.Fatalf("offered %q, want %q", h.app.clone.parent, parent)
	}

	h = newHarness(t)
	h.app.Settings.CloneParent = filepath.Join(parent, "gone")
	h.app.ShowClone()
	h.until("the folder", func() bool { return h.app.clone.parent != "" })
	if h.app.clone.parent == h.app.Settings.CloneParent {
		t.Fatal("a folder no longer there was offered")
	}
}

// A folder typed by hand stays as the URL changes.
func TestCloneFolderTyped(t *testing.T) {
	h := newHarness(t)
	h.app.clone.parent, h.app.clone.looked = t.TempDir(), true
	h.app.ShowClone()
	h.tt.Frame()
	h.app.clone.dir = "/somewhere/else"
	h.tt.Frame()
	h.app.clone.url = "https://example.com/owner/repo.git"
	h.tt.Frame()
	if h.app.clone.dir != "/somewhere/else" {
		t.Fatalf("folder typed replaced by %q", h.app.clone.dir)
	}
}
