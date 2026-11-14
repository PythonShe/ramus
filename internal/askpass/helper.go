package askpass

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

// IsHelper reports whether this process was started by git or ssh as the
// app's askpass helper.
func IsHelper() bool { return os.Getenv(envSocket) != "" && os.Getenv(envToken) != "" }

// Helper asks the app the question in args, prints the answer for git
// or ssh to read and returns the exit code: 0 answered, 1 declined or
// not asked. It touches nothing of the app's but the socket.
func Helper(args []string, stdout io.Writer) int {
	prompt := strings.Join(args, " ")
	if len(prompt) > maxMessage/2 {
		prompt = prompt[:maxMessage/2]
	}
	dir, _ := os.Getwd()
	q := Question{Prompt: prompt, Kind: kindOf(prompt, os.Getenv("SSH_ASKPASS_PROMPT")), Dir: dir, Token: os.Getenv(envToken)}
	conn, err := net.Dial("unix", os.Getenv(envSocket))
	if err != nil {
		fmt.Fprintln(os.Stderr, "askpass: the app is not running:", err)
		return 1
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(q); err != nil {
		return 1
	}
	// No deadline: the user takes the time they take, and git's own
	// timeout ends this process with the command.
	line, err := bufio.NewReader(io.LimitReader(conn, maxMessage)).ReadBytes('\n')
	var a answer
	if err != nil || json.Unmarshal(line, &a) != nil || !a.OK {
		return 1
	}
	if q.Kind != Confirm {
		fmt.Fprintln(stdout, a.Answer)
	}
	return 0
}
