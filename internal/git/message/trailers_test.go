package message

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestTrailersMatchGit compares Trailers with git interpret-trailers
// --parse, which prints each trailer as "Key: value".
func TestTrailersMatchGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	msgs := []string{
		"Subject only",
		"Co-authored-by: A <a@x>",
		"Subject\n\nBody text.\n\nCo-authored-by: Ann Lee <ann@example.com>\nCo-authored-by: Bo <bo@example.com>\n",
		"Subject\n\nBody text.\nCo-authored-by: Ann <ann@example.com>\n",
		"Subject\n\nBody\n\nSigned-off-by: A <a@x>\nnot a trailer\nCo-authored-by: B <b@x>\n",
		"Subject\n\nBody\n\nsome text\nmore text\nCo-authored-by: B <b@x>\n",
		"Subject\n\nBody\n\nReviewed-by: A <a@x>\n  continued here\nCo-authored-by: C <c@x>\n\n\n",
		"Subject\n\nBody\n\nCo-authored-by: C <c@x>\n---\nnot: part\n",
		"Subject\n\nBody\n\nFixes: #12\nco-authored-by: d <D@X.org>\r\n",
		"Subject\n\nBody\n\nnot a trailer: because spaces in key\n",
		"Subject\n\n(cherry picked from commit abc)\nfree text\nfree text\nfree text\nAcked-by: E <e@x>\n",
	}
	for _, m := range msgs {
		cmd := exec.Command("git", "interpret-trailers", "--parse")
		cmd.Stdin = strings.NewReader(m)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var got strings.Builder
		for _, tr := range Trailers([]byte(m)) {
			fmt.Fprintf(&got, "%s: %s\n", tr.Key, tr.Value)
			if !strings.HasPrefix(m[tr.Start:], tr.Key) {
				t.Errorf("%q: trailer %q starts at %d", m, tr.Key, tr.Start)
			}
		}
		if got.String() != string(out) {
			t.Errorf("%q:\n got %q\nwant %q", m, got.String(), out)
		}
	}
}

func TestCoAuthors(t *testing.T) {
	got := CoAuthors([]byte("S\n\nB\n\nCo-authored-by: Ann Lee <ann@example.com>\nSigned-off-by: X <x@x>\nCo-Authored-By:Bo<bo@x>\nCo-authored-by: nobody\n"))
	want := []Person{{"Ann Lee", "ann@example.com"}, {"Bo", "bo@x"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
