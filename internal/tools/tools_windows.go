package tools

import (
	"os"
	"path/filepath"
)

func macApp(name string, kind Kind, bundle string) Tool {
	return Tool{Name: name, Kind: kind, Argv: []string{bundle}}
}

// Detect lists editors and terminals at their installed locations; PATH
// on Windows is searched after the current directory, so only full paths
// are used.
func Detect() []Tool {
	local, pf, pf86 := os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")
	sys := os.Getenv("SystemRoot")
	j := func(parts ...string) string {
		if parts[0] == "" {
			return ""
		}
		return filepath.Join(parts...)
	}
	var out []Tool
	add := func(name string, kind Kind, how func([]string, string, int) []string, cands ...string) {
		if p, ok := first(cands...); ok {
			out = append(out, Tool{Name: name, Kind: kind, Argv: []string{p}, how: how})
		}
	}
	add("Visual Studio Code", Editor, nil, j(local, "Programs", "Microsoft VS Code", "Code.exe"), j(pf, "Microsoft VS Code", "Code.exe"))
	add("Cursor", Editor, nil, j(local, "Programs", "cursor", "Cursor.exe"))
	add("Zed", Editor, nil, j(local, "Programs", "Zed", "Zed.exe"))
	add("Sublime Text", Editor, nil, j(pf, "Sublime Text", "sublime_text.exe"))
	add("Notepad++", Editor, nil, j(pf, "Notepad++", "notepad++.exe"), j(pf86, "Notepad++", "notepad++.exe"))
	add("Notepad", Editor, nil, j(sys, "System32", "notepad.exe"))
	add("Windows Terminal", Terminal, func(a []string, t string, _ int) []string { return append(a, "-d", t) },
		j(local, "Microsoft", "WindowsApps", "wt.exe"))
	add("PowerShell", Terminal, func(a []string, _ string, _ int) []string { return a },
		j(pf, "PowerShell", "7", "pwsh.exe"), j(sys, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	add("Command Prompt", Terminal, func(a []string, _ string, _ int) []string { return append(a, "/k") },
		j(sys, "System32", "cmd.exe"))
	return out
}

// Reveal shows path selected in Explorer.
func Reveal(path string) error {
	ex := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	return Tool{Name: "Explorer", Argv: []string{ex}, how: func(a []string, t string, _ int) []string {
		return append(a, "/select,"+t)
	}}.Open(path, 0)
}

// OpenDefault opens path in the app Windows has for it.
func OpenDefault(path string) error {
	ex := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	return Tool{Name: "default", Argv: []string{ex}}.Open(path, 0)
}
