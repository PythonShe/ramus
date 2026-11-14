package licenses

import (
	"os/exec"
	"strings"
	"testing"
)

// Every module this platform's build links has its license here, at the
// version linked: run generate.go when a dependency changes.
func TestCoversTheBuild(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]string{}
	for _, n := range all {
		if n.Text == "" {
			t.Errorf("%s has no text", n.Name)
		}
		have[n.Name] = n.Version
	}
	for _, name := range []string{"Ramus", "Go", "Symbols file icons"} {
		if _, ok := have[name]; !ok {
			t.Errorf("no license for %s", name)
		}
	}
	cmd := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}", ".")
	cmd.Dir = "../.."
	b, err := cmd.Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		path, version, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if got, ok := have[path]; !ok || got != strings.TrimPrefix(version, "v") {
			t.Errorf("%s %s has no license here (have %q): run go run internal/licenses/generate.go", path, version, got)
		}
	}
}
