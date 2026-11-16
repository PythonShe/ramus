package ui

import "testing"

func TestReflowBody(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"one line", "Just one line.", "Just one line."},
		{"a paragraph", "The reader keeps the pack mapped\nwhile the history is open,\nand lets it go after.",
			"The reader keeps the pack mapped while the history is open, and lets it go after."},
		{"paragraphs", "First paragraph\nof two lines.\n\nSecond paragraph\nalso of two.",
			"First paragraph of two lines.\n\nSecond paragraph also of two."},
		{"trailing spaces", "broken here  \nand joined", "broken here and joined"},
		{"dash list", "Changes:\n- one item\n- another item\nthat goes on",
			"Changes:\n- one item\n- another item\nthat goes on"},
		{"star and plus lists", "Before\n* star\n+ plus", "Before\n* star\n+ plus"},
		{"numbered lists", "Steps\n1. first\n2) second\n10. tenth", "Steps\n1. first\n2) second\n10. tenth"},
		{"a number in prose", "It took\n2.5 seconds", "It took 2.5 seconds"},
		{"indented code", "Run it:\n\n    go test ./...\n    go vet ./...\nand see.",
			"Run it:\n\n    go test ./...\n    go vet ./...\nand see."},
		{"tab indented", "See\n\tcode\nafter", "See\n\tcode\nafter"},
		{"quote", "He said\n> keep it\n> short\nand left", "He said\n> keep it\n> short\nand left"},
		{"fence", "Example\n```\nline one\nline two\n```\nafter the fence\nand more",
			"Example\n```\nline one\nline two\n```\nafter the fence and more"},
		{"trailers", "Fix the cache\nthat grew.\n\nSigned-off-by: A <a@example.com>\nCo-authored-by: B <b@example.com>",
			"Fix the cache that grew.\n\nSigned-off-by: A <a@example.com>\nCo-authored-by: B <b@example.com>"},
		{"trailer after prose", "Short fix.\nFixes: #12", "Short fix.\nFixes: #12"},
		{"a colon in prose", "see also: the\nmanual", "see also: the manual"},
		{"blank lines kept", "a\n\n\nb", "a\n\n\nb"},
	} {
		if got := reflowBody(tc.in); got != tc.want {
			t.Errorf("%s: reflowBody(%q)\n got %q\nwant %q", tc.name, tc.in, got, tc.want)
		}
	}
}
