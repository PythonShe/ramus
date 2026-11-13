package mailmap

import (
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// Lookup gives what git check-mailmap gives, over every form of line.
func TestLookupMatchesGit(t *testing.T) {
	file := `# comment
Proper One <one@example.com>
<two-proper@example.com> <two@example.com>
Proper Three <three-proper@example.com> <three@example.com>
Proper Four <four-proper@example.com> Commit Four <four@example.com>
Other Four <four-other@example.com> Second Four <FOUR@example.com>
Later Name <one@example.com>
<five-new@example.com> <five@example.com>
Five Name <five@example.com>
Spaces   Here   <six@example.com>   trailing text
broken line without brackets
`
	r := gittest.Init(t)
	r.Write(".mailmap", file)
	var m Map
	m.Add([]byte(file))
	for _, id := range []string{
		"Someone <one@example.com>",
		"Someone <ONE@Example.com>",
		"Two <two@example.com>",
		"Three <three@example.com>",
		"Commit Four <four@example.com>",
		"commit four <four@example.com>",
		"Second Four <four@example.com>",
		"Nobody Four <four@example.com>",
		"Five <five@example.com>",
		"Six <six@example.com>",
		"Stranger <stranger@example.com>",
	} {
		want := strings.TrimSpace(r.Git("check-mailmap", id))
		name, email, _, _ := nameAndEmail(id)
		n, e := m.Lookup(name, email)
		if got := n + " <" + e + ">"; got != want {
			t.Errorf("%s: got %q, git %q", id, got, want)
		}
	}
}
