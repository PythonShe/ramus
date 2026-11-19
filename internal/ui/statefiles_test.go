package ui

import (
	"errors"
	"strings"
	"testing"

	"ramus/internal/statefile"
)

// The notice of a state file that did not read names the file, and
// where it went when it was kept aside.
func TestLoadNoticeNamesTheFile(t *testing.T) {
	bad := errors.New("line 3: invalid character")
	for _, c := range []struct {
		err  *statefile.LoadError
		want []string
	}{
		{&statefile.LoadError{Path: "/s/settings.json", Err: bad, KeptAs: "/s/settings.json.broken"}, []string{"/s/settings.json", "/s/settings.json.broken", "line 3"}},
		{&statefile.LoadError{Path: "/s/library.json", Err: bad, Partial: true, KeptAs: "/s/library.json.broken"}, []string{"/s/library.json", "/s/library.json.broken"}},
		{&statefile.LoadError{Path: "/s/settings.json", Err: bad, Partial: true}, []string{"/s/settings.json", "line 3"}},
		{&statefile.LoadError{Path: "/s/layout.json", Err: bad, KeepErr: errors.New("read-only")}, []string{"/s/layout.json", "line 3"}},
	} {
		got := LoadNotice(c.err)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%q lacks %q", got, w)
			}
		}
	}
}
