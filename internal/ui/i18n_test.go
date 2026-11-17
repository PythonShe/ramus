package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// Every text a user reads comes from the catalog in ramus/locales, so the
// app can be translated. This finds literal text where the views say
// something: given to a control, a label, a dialog or a format.

// textFuncs are the functions of package ui whose string arguments a
// user reads.
var textFuncs = map[string]bool{
	"AccordionItem": true, "AlertDialog": true, "Badge": true, "Button": true, "Checkbox": true,
	"Collapsible": true, "Field": true, "Fieldset": true, "Link": true, "MenuButton": true,
	"PopoverMenuButton": true, "PrimaryButton": true, "PrimarySplitButton": true, "Segmented": true,
	"Select": true, "SidebarItem": true, "SidebarSection": true, "SplitButton": true, "Tabs": true,
	"Text": true, "Textf": true, "Toggle": true, "TreeItem": true,
}

// textMethods are methods whose string arguments a user reads, whatever
// their receiver.
var textMethods = map[string]bool{
	"Announce": true, "Description": true, "Error": true, "Item": true, "Label": true,
	"Placeholder": true, "RoleDescription": true, "Subtitle": true, "Title": true, "Toast": true,
	"Tooltip": true, "Trailing": true, "Notify": true,
}

// textFields are the fields and variables the views keep text in before
// they show it.
var textFields = map[string]bool{
	"title": true, "message": true, "button": true, "label": true, "labels": true, "Name": true,
	"what": true, "msg": true, "reason": true, "hint": true, "text": true, "Label": true,
}

// untranslated are literals that are the same in every language: names,
// git's own words, symbols.
var untranslated = map[string]bool{
	"HEAD": true, "Ramus": true, "monospace": true, "×": true, "…": true, "git": true,
	// Examples in placeholders: a URL, paths and a pattern read the same
	// in every language.
	`\bPROJ-(\d+)\b`: true,
	"https://tracker.example.com/browse/PROJ-$1": true,
	"/usr/local/bin/code --wait":                 true,
	"/Applications/Ghostty.app":                  true,
}

func TestTextComesFromTheCatalog(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var found []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		report := func(e ast.Expr) {
			for _, lit := range literals(e) {
				found = append(found, fset.Position(lit.Pos()).String()+" "+lit.Value)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if saysText(n) {
					for _, a := range n.Args {
						report(a)
					}
				}
			case *ast.KeyValueExpr:
				if k, ok := n.Key.(*ast.Ident); ok && textFields[k.Name] {
					report(n.Value)
				}
			case *ast.AssignStmt:
				for i, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok && textFields[id.Name] && i < len(n.Rhs) {
						report(n.Rhs[i])
					}
				}
			}
			return true
		})
	}
	if len(found) > 0 {
		t.Errorf("%d texts not in the catalog (locales/en.json):\n%s", len(found), strings.Join(found, "\n"))
	}
}

// saysText reports whether call shows its string arguments to a user.
func saysText(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if pkg, ok := sel.X.(*ast.Ident); ok {
		switch pkg.Name {
		case "ui":
			return textFuncs[sel.Sel.Name]
		case "fmt":
			return sel.Sel.Name == "Sprintf" || sel.Sel.Name == "Errorf"
		case "errors":
			return sel.Sel.Name == "New"
		}
	}
	return textMethods[sel.Sel.Name]
}

// literals returns the string literals with words in them that e is made
// of, through concatenations and slices of them.
func literals(e ast.Expr) []*ast.BasicLit {
	var out []*ast.BasicLit
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit, *ast.CallExpr:
			// What a function or a call returns is checked where it is
			// made.
			return false
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return false
			}
			s, err := strconv.Unquote(n.Value)
			if err == nil && hasWord(s) && !untranslated[s] {
				out = append(out, n)
			}
		}
		return true
	})
	return out
}

// hasWord reports whether s has two letters in a row: a word, not a
// separator or a format verb.
func hasWord(s string) bool {
	s = strings.NewReplacer("%s", "", "%d", "", "%q", "", "%v", "").Replace(s)
	run := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			if run++; run >= 2 {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}
