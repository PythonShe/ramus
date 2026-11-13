package ui

import (
	"strings"
	"testing"
)

func TestGroupRefs(t *testing.T) {
	names := []string{"feature/a", "feature/b", "feature/ui/c", "fix/d", "main"}
	show := func(rows []refRow) string {
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(strings.Repeat(" ", r.depth) + r.label)
			if r.idx < 0 {
				b.WriteString("/")
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	if got, want := show(groupRefs(names, "", nil)), "feature/\n a\n b\n ui/\n  c\nfix/\n d\nmain\n"; got != want {
		t.Errorf("open:\n%s", got)
	}
	if got, want := show(groupRefs(names, "", map[string]bool{"feature/ui": true, "fix": true})), "feature/\n a\n b\n ui/\nfix/\nmain\n"; got != want {
		t.Errorf("closed:\n%s", got)
	}
	if got, want := show(groupRefs(names, "", map[string]bool{"feature": true})), "feature/\nfix/\n d\nmain\n"; got != want {
		t.Errorf("closed outer:\n%s", got)
	}
	// A filter opens every folder and keeps only what matches.
	if got, want := show(groupRefs(names, "C", map[string]bool{"feature": true})), "feature/\n ui/\n  c\n"; got != want {
		t.Errorf("filtered:\n%s", got)
	}
}
