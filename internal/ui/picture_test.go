package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	h.until("open", h.shows("No local changes"))
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

// A picture stored in Git LFS, which git diffs as its pointers, shows
// before and after from the local LFS store; a side never downloaded
// says so.
func TestPictureDiffFromLFS(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("no git-lfs:", err)
	}
	h := newHarness(t)
	r := h.repoWith()
	// What git lfs install writes, in the repository's own config.
	for _, kv := range [][2]string{{"clean", "git-lfs clean -- %f"}, {"smudge", "git-lfs smudge -- %f"},
		{"process", "git-lfs filter-process"}, {"required", "true"}} {
		r.Git("config", "filter.lfs."+kv[0], kv[1])
	}
	r.Write(".gitattributes", "*.png filter=lfs diff=lfs merge=lfs -text\n")
	r.Write("logo.png", pngOf(t, 40, 20, color.RGBA{200, 0, 0, 255}))
	r.Write("gone.png", pngOf(t, 8, 8, color.RGBA{0, 200, 0, 255}))
	r.Git("add", ".gitattributes", "logo.png", "gone.png")
	r.Commit("logo")
	if p := r.Git("cat-file", "-p", "HEAD:logo.png"); !strings.HasPrefix(p, "version https://git-lfs") {
		t.Fatalf("not a pointer: %q", p)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	r.Write("logo.png", pngOf(t, 64, 32, color.RGBA{0, 0, 200, 255}))
	r.Write("gone.png", pngOf(t, 16, 16, color.RGBA{0, 0, 200, 255}))
	// gone.png's old content was never downloaded: its object goes once
	// no file in the work tree holds it, as git's clean filter, run on
	// such a file, would store it again.
	oid := strings.TrimPrefix(strings.Fields(strings.Split(r.Git("cat-file", "-p", "HEAD:gone.png"), "\n")[1])[1], "sha256:")
	if err := os.Remove(filepath.Join(r.GitDir(), "lfs", "objects", oid[:2], oid[2:4], oid)); err != nil {
		t.Fatal(err)
	}
	// Both writes, which a status run between them can see one of.
	h.until("the changes", func() bool { return h.tt.HasText("logo.png") && h.tt.HasText("gone.png") })
	h.click("logo.png")
	h.until("the pictures", h.shows("40 × 20, 95 bytes"))
	if !h.tt.HasText("64 × 32") || h.tt.HasText("version https://git-lfs") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	h.click("gone.png")
	h.until("the side not downloaded", h.shows("Not downloaded from Git LFS"))
	if !h.tt.HasText("16 × 16") {
		t.Fatalf("texts %q", h.tt.Texts())
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
