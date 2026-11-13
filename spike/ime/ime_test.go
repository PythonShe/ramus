// Package ime records how mygo's text controls handle input-method
// composition, headless: Tester.Compose sends marked text as an input
// method does, Tester.Type commits it.
//
// The platform layers hold back keys an input method takes (macOS
// withholds keyDown while text is marked; GTK filters through the IM
// context; Windows sees VK_PROCESSKEY), so a real Enter while composing
// never reaches the controls there. These tests show what the controls do
// when a key does reach them while composing, which is what a platform
// that fails to hold it back would produce.
package ime

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestTextInput(t *testing.T) {
	var value string
	submits := 0
	composing := false
	tt := ui.NewTester(func(c *ui.Context) {
		in := ui.TextInput(c, &value).AutoFocus()
		composing = in.Composing()
		if in.Submitted() {
			submits++
		}
	}, 400, 200)
	tt.Frame()
	tt.Compose("にほ", 2)
	tt.Frame()
	t.Logf("while composing: value %q, Composing()=%v", value, composing)
	if !composing {
		t.Error("Composing() false while marked text shows")
	}
	tt.Type("日本")
	tt.Frame()
	if value != "日本" {
		t.Fatalf("committed %q", value)
	}
	tt.Compose("ご", 1)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	t.Logf("Enter delivered while composing: value %q, submits %d", value, submits)
}

func TestCodeEditor(t *testing.T) {
	st := &editor.State{Buffer: editor.NewBuffer("")}
	tt := ui.NewTester(func(c *ui.Context) {
		editor.View(c, st).Fill().AutoFocus()
	}, 600, 400)
	tt.Frame()
	tt.Compose("한", 1)
	tt.Frame()
	tt.Compose("한국", 2)
	tt.Type("한국어")
	tt.Frame()
	if got := st.Buffer.String(); got != "한국어" {
		t.Fatalf("committed %q", got)
	}
	tt.Compose("中", 1)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	t.Logf("Enter delivered while composing in the editor: buffer %q", st.Buffer.String())
}

func TestDiffFind(t *testing.T) {
	d := &editor.DiffState{Old: editor.NewBuffer("猫 dog\n猫\n"), New: editor.NewBuffer("猫 cat\n猫\n")}
	tt := ui.NewTester(func(c *ui.Context) {
		editor.DiffView(c, d).Grow(1).FillWidth()
	}, 800, 400)
	tt.Frame()
	d.Find("")
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	activeBefore, _ := tt.TextInputActive()
	if err := tt.Click("Find"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	active, noIME := tt.TextInputActive()
	t.Logf("find field focused when Find opens it: %v; after a click: %v", activeBefore, active)
	tt.Compose("ねこ", 2)
	tt.Frame()
	tt.Type("猫")
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	query := ""
	for _, a := range tt.AccessAll() {
		if a.Name == "Find" && a.Value != "" {
			query = a.Value
		}
	}
	t.Logf("find field takes text=%v (input methods off=%v); query after committing 猫: %q", active, noIME, query)
	if query != "猫" {
		for _, a := range tt.AccessAll() {
			t.Logf("%v %q %q", a.Role, a.Name, a.Value)
		}
		t.Errorf("find field holds %q", query)
	}
}
