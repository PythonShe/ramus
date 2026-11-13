package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ramus/internal/git/gittest"
)

func pngOf(t *testing.T, w, h int, c color.Color) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A picture's change shows it before and after.
func TestPictureDiff(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("logo.png", pngOf(t, 40, 20, color.RGBA{200, 0, 0, 255}))
	r.Commit("logo")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	r.Write("logo.png", pngOf(t, 64, 32, color.RGBA{0, 0, 200, 255}))
	h.until("the change", h.shows("logo.png"))
	h.click("logo.png")
	h.until("the pictures", h.shows("Before"))
	for _, want := range []string{"After", "40 × 20, 95 bytes", "64 × 32"} {
		if !h.tt.HasText(want) {
			t.Fatalf("no %q in %q", want, h.tt.Texts())
		}
	}
}

// A submodule's change lists the commits it moves over.
func TestSubmoduleDiff(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	sub := gittest.Init(t)
	sub.Write("s", "0")
	sub.Commit("first")
	r.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "lib")
	r.Commit("with lib")
	inner := filepath.Join(r.Dir, "lib")
	for _, msg := range []string{"second", "third"} {
		os.WriteFile(filepath.Join(inner, "s"), []byte(msg), 0o644)
		gitIn(t, inner, "commit", "-q", "-a", "-m", msg)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the change", h.shows("modified lib"))
	// The row, not the submodule in the sidebar, which opens it.
	h.click("modified lib")
	h.until("the range", h.shows("2 commits added"))
	for _, want := range []string{"second", "third"} {
		if !h.tt.HasText(want) {
			t.Fatalf("no %q in %q", want, h.tt.Texts())
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
