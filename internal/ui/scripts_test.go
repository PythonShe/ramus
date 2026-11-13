package ui

import "testing"

// File names, messages and diffs in Chinese, Japanese, Korean, Arabic and
// emoji reach the views as they are, for the platform's text engine to
// draw through its font fallback.
func TestScripts(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	names := []string{"日本語のファイル.txt", "مرحبا بالعالم.md", "한국어 문서.txt", "party 🎉 emoji.txt", "简体中文.go"}
	for _, n := range names {
		r.Write(n, n+"\n")
	}
	subject := "提交: 日本語 / 한국어 / مرحبا / 🎉 👨‍👩‍👧"
	r.Commit(subject)
	r.Write(names[0], "こんにちは、世界！\n")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", func() bool { return hasExact(h, names[0]) })
	h.click(names[0])
	pv := &h.app.current().cv.diff.pv
	h.until("the patch", patchHas(pv, "こんにちは、世界！", names[0]))

	h.click("History")
	h.until("the commit", func() bool { return hasExact(h, subject) })
	h.click(subject)
	h.until("its files", h.shows("5 files changed"))
	for _, n := range names {
		if !hasExact(h, n) {
			t.Errorf("%q is not shown as it is: %q", n, h.tt.Texts())
		}
	}
}
