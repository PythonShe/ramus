package ui

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestScreenshot draws the History view of a real repository into a PNG,
// for the cards in assets/cards; it is skipped unless RAMUS_SCREENSHOT
// names the file to write. RAMUS_SCREENSHOT_REPO is the repository, which
// is only read; RAMUS_SCREENSHOT_CLICK lists the texts to click in turn,
// separated by "|", a commit's subject and then one of its files; and
// RAMUS_SCREENSHOT_LIGHT asks for the light theme.
func TestScreenshot(t *testing.T) {
	out, dir := os.Getenv("RAMUS_SCREENSHOT"), os.Getenv("RAMUS_SCREENSHOT_REPO")
	if out == "" {
		t.Skip("RAMUS_SCREENSHOT is not set")
	}
	h := newHarness(t)
	t.Cleanup(h.closeAll)
	h.tt = ui.NewTesterWith(h.app.View, 1440, 900, ui.TesterOptions{Dark: os.Getenv("RAMUS_SCREENSHOT_LIGHT") == ""})
	h.tt.SetScale(2)
	// Trust lives in the harness's own store, so nothing of the user's changes.
	h.trust.Set(filepath.Join(dir, ".git"), true)
	h.app.Open(dir)
	h.until("the repository", h.shows("History"))
	h.click("History")
	h.until("the whole history", func() bool {
		for _, s := range h.tt.Texts() {
			if strings.HasSuffix(s, " commits") && !strings.Contains(s, "so far") {
				return true
			}
		}
		return false
	})
	for _, text := range strings.Split(os.Getenv("RAMUS_SCREENSHOT_CLICK"), "|") {
		if text == "" {
			continue
		}
		h.until(text, h.shows(text))
		h.click(text)
		h.pumpFor(1500 * time.Millisecond)
	}
	h.pumpFor(time.Second)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, h.tt.Image()); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// pumpFor runs the app's updates and frames for d. A click starts reads in
// the background, and settle cannot wait for them here: a frame stays
// asked for while the Tester's clock stands still.
func (h *harness) pumpFor(d time.Duration) {
	end := time.Now().Add(d)
	h.until("the wait to end", func() bool { return time.Now().After(end) })
}
