package tools

import "strconv"

type known struct {
	name, program string
	kind          Kind
	how           func(argv []string, target string, line int) []string
}

func gotoLine(flag string) func([]string, string, int) []string {
	return func(argv []string, target string, line int) []string {
		if line > 0 {
			return append(argv, flag, target+":"+strconv.Itoa(line))
		}
		return append(argv, target)
	}
}

func dirFlag(flag string) func([]string, string, int) []string {
	return func(argv []string, target string, _ int) []string { return append(argv, flag+target) }
}

var linuxTools = []known{
	{"Visual Studio Code", "code", Editor, gotoLine("--goto")},
	{"Cursor", "cursor", Editor, gotoLine("--goto")},
	{"Zed", "zed", Editor, nil},
	{"Sublime Text", "subl", Editor, nil},
	{"GoLand", "goland", Editor, nil},
	{"IntelliJ IDEA", "idea", Editor, nil},
	{"Kate", "kate", Editor, nil},
	{"gedit", "gedit", Editor, nil},
	{"GNOME Terminal", "gnome-terminal", Terminal, dirFlag("--working-directory=")},
	{"Ptyxis", "ptyxis", Terminal, dirFlag("--working-directory=")},
	{"Konsole", "konsole", Terminal, dirFlag("--workdir=")},
	{"Xfce Terminal", "xfce4-terminal", Terminal, dirFlag("--working-directory=")},
	{"kitty", "kitty", Terminal, dirFlag("--directory=")},
	{"Alacritty", "alacritty", Terminal, func(a []string, t string, _ int) []string { return append(a, "--working-directory", t) }},
	{"WezTerm", "wezterm", Terminal, func(a []string, t string, _ int) []string { return append(a, "start", "--cwd", t) }},
	{"Ghostty", "ghostty", Terminal, dirFlag("--working-directory=")},
	{"foot", "foot", Terminal, func(a []string, t string, _ int) []string { return append(a, "-D", t) }},
	{"xterm", "xterm", Terminal, func(a []string, _ string, _ int) []string { return a }},
}

func macApp(name string, kind Kind, bundle string) Tool {
	return Tool{Name: name, Kind: kind, Argv: []string{bundle}}
}

// Detect lists the editors and terminals on PATH.
func Detect() []Tool {
	var out []Tool
	for _, k := range linuxTools {
		if p, ok := lookPath(k.program); ok {
			out = append(out, Tool{Name: k.name, Kind: k.kind, Argv: []string{p}, how: k.how})
		}
	}
	return out
}

// Reveal opens the folder holding path; file managers on Linux do not
// agree on a way to select a file.
func Reveal(path string) error {
	return OpenDefault(dir(path))
}

// OpenDefault opens path in the app the desktop has for it.
func OpenDefault(path string) error {
	p, ok := lookPath("xdg-open")
	if !ok {
		return errNoOpener
	}
	return Tool{Name: "default", Argv: []string{p}}.Open(path, 0)
}
