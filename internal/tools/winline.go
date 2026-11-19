package tools

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// The command lines of Windows, built here rather than by Go where Go's
// quoting, made for programs that split their command line as
// CommandLineToArgvW does, is the wrong one. They are plain functions, so
// that they are tested on every system.

// isBatch reports whether Windows runs program through cmd.exe: a .bat
// or .cmd file, in any case, and with the dots and spaces Windows drops
// from the end of a name.
func isBatch(program string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimRight(program, ". ")))
	return ext == ".bat" || ext == ".cmd"
}

// ErrBatchArgument is an argument that cannot be handed to a batch file
// as it is: cmd.exe expands % in it, even quoted, a quote or a line
// break would end it, and ! is expanded, and ^ taken away beside it, by
// a script that turns delayed expansion on (setlocal
// EnableDelayedExpansion), which cmd.exe's /v:off cannot keep it from:
// whether an argument must be escaped for it depends on the script.
var ErrBatchArgument = errors.New("tools: a batch file cannot be given this argument safely")

// batchLine is the command line that has shell (cmd.exe) run the batch
// file argv[0] with the arguments argv[1:], each passed as one argument
// whatever it holds: each is quoted, inside which & | < > ^ ( ) are
// plain characters to cmd.exe. /d leaves out the AutoRun commands of the
// registry, /v:off the expansion of !, which the registry may turn on,
// while the line is read; /s /c strips the outer quotes only. One that
// holds % or ! is refused (ErrBatchArgument).
func batchLine(shell string, argv []string) (string, error) {
	var b strings.Builder
	b.WriteString(`"` + shell + `" /d /e:on /v:off /s /c "`)
	for i, a := range argv {
		if strings.ContainsAny(a, `"%!`) || strings.ContainsFunc(a, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return "", fmt.Errorf("%w: %q", ErrBatchArgument, a)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		b.WriteString(a)
		// A program the batch file hands the argument on to reads \" as a
		// quote: the backslashes before the closing one are doubled.
		b.WriteString(strings.Repeat(`\`, len(a)-len(strings.TrimRight(a, `\`))))
		b.WriteByte('"')
	}
	b.WriteByte('"')
	return b.String(), nil
}

// revealLine is the command line that has Explorer show path selected.
// Explorer reads its own command line, and takes the path quoted after
// the comma, "/select," apart from it; quoted whole, as Go would quote
// the argument for a path with a space, it opens another folder.
func revealLine(explorer, path string) (string, error) {
	if strings.Contains(path, `"`) {
		return "", fmt.Errorf("tools: %q cannot be shown", path)
	}
	return `"` + explorer + `" /select,"` + path + `"`, nil
}

// wtFolder is a folder as Windows Terminal's -d takes it: wt.exe reads a
// ; in its arguments as the start of another command unless escaped.
func wtFolder(path string) string { return strings.ReplaceAll(path, ";", `\;`) }
