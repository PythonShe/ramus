package fileicon

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestName(t *testing.T) {
	for p, want := range map[string]string{
		"cmd/main.go":      "go",
		"go.mod":           "go-pink",
		"web/App.TSX":      "react-ts",
		"types/index.d.ts": byExtension["d.ts"],
		"site/index.php":   "php",
		"notes/plan.zzz":   defaultIcon,
		"LICENSE":          "license",
		".env":             Name("x/.env"),
		"src/a.b/c":        defaultIcon,
		"Dockerfile":       "docker",
		"archive.tar.gz":   byExtension["tar.gz"],
	} {
		if got := Name(p); got != want {
			t.Errorf("Name(%q) = %q, want %q", p, got, want)
		}
	}
}

// Every icon a table names is packed and parses.
func TestEveryIconParses(t *testing.T) {
	names := map[string]bool{defaultIcon: true}
	for _, m := range []map[string]string{byName, byExtension, byLanguage} {
		for _, n := range m {
			names[n] = true
		}
	}
	unpack.Do(func() { svgs = read(packed) })
	for n := range names {
		if _, ok := svgs[n+".svg"]; !ok {
			t.Errorf("icon %q is not packed", n)
			continue
		}
		if _, err := ui.ParseSVG(svgs[n+".svg"]); err != nil {
			t.Errorf("icon %q does not parse: %v", n, err)
		}
	}
}
