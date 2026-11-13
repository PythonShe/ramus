// Package access records what a screen reader can reach in the views the
// spike found fast enough: the history list with its drawn graph column,
// and editor.DiffView. It reads mygo's platform-neutral accessibility tree
// through ui.Tester; what VoiceOver, UIA and AT-SPI receive from it is
// mygo's platform layers' work, read from source.
package access

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestHistoryRows(t *testing.T) {
	const n = 100_000
	var list ui.ListState
	tt := ui.NewTester(func(c *ui.Context) {
		list.Label = func(i int) string { return fmt.Sprintf("Commit %d", i) }
		ui.List(c, &list, n, func(i int) {
			ui.Row(c).Height(24).Children(func() {
				desc := "commit on the main line"
				if i%10 == 0 {
					desc = "merge of main and topic-" + fmt.Sprint(i)
				}
				ui.Box(c).Width(120).Height(24).Role(ui.RoleImage).Label(desc).
					Draw(func(p *ui.Painter, r ui.Rect) { p.Fill(r, ui.Hex("#888888"), 0) })
				ui.Text(c, fmt.Sprintf("Commit %d", i))
			})
		}).Grow(1)
	}, 800, 600)
	tt.Frame()
	list.ScrollTo(50_000, ui.Start)
	tt.Frame()
	tt.Frame()
	var items, images int
	for _, a := range tt.AccessAll() {
		switch a.Role {
		case ui.RoleListItem:
			if items == 0 {
				t.Logf("first row in view: %q, %d of %d", a.Name, a.PosInSet, a.SetSize)
				if a.SetSize != n {
					t.Errorf("set size %d, want %d", a.SetSize, n)
				}
			}
			items++
		case ui.RoleImage:
			if images < 3 {
				t.Logf("graph cell reads %q", a.Name)
			}
			images++
		}
	}
	if items == 0 || images == 0 {
		t.Fatalf("rows %d, graph cells %d reachable", items, images)
	}
	t.Logf("%d rows and %d graph cells reachable in view", items, images)
}

func TestDiffRows(t *testing.T) {
	var old, new strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&old, "line %d\n", i)
		if i%20 == 0 {
			fmt.Fprintf(&new, "line %d changed\n", i)
		} else {
			fmt.Fprintf(&new, "line %d\n", i)
		}
	}
	d := &editor.DiffState{Old: editor.NewBuffer(old.String()), New: editor.NewBuffer(new.String()), Layout: editor.DiffSplit}
	tt := ui.NewTester(func(c *ui.Context) {
		editor.DiffView(c, d).Grow(1).FillWidth()
	}, 1000, 600)
	tt.Frame()
	roles := map[string]int{}
	for _, a := range tt.AccessAll() {
		roles[fmt.Sprint(a.Role)]++
		if a.Name == "New text" {
			t.Logf("new side: role %v, value of %d bytes, description %q", a.Role, len(a.Value), a.Description)
		}
	}
	t.Logf("roles in the diff view: %v", roles)
	if roles[fmt.Sprint(ui.RoleInsertion)] == 0 && roles[fmt.Sprint(ui.RoleDeletion)] == 0 {
		t.Log("no per-line nodes and no insertion/deletion roles: changed lines are not told apart for assistive technology")
	}
}
