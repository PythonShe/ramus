// Package askpass lets git, ssh and the tools they start ask the user
// for a password, a passphrase or a yes or no, in the app's window.
//
// The app's own program is the helper: git and ssh run it as
// SSH_ASKPASS with the question as its argument. Run as the helper (see
// IsHelper), it hands the question to the app over a socket only the app
// listens on, prints the answer and exits; the app shows the question
// and answers it.
//
// Only SSH_ASKPASS is set: git asks through it when neither GIT_ASKPASS
// nor core.askPass is set, so an askpass the user configured still wins.
package askpass

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ramus/internal/git/redact"
)

// The helper finds the app through these, set in the environment of
// every git the app runs that may ask.
const (
	envSocket = "RAMUS_ASKPASS_SOCKET"
	envToken  = "RAMUS_ASKPASS_TOKEN"
)

// Bounds on a question: a prompt is a line or two from git or ssh, and
// no more than a few are asked at once.
const (
	maxMessage = 64 << 10
	maxOpen    = 8
	// readLimit bounds how long a helper takes to send its question.
	readLimit = 5 * time.Second
)

// Kind is what a question asks for.
type Kind int

const (
	// Secret is a password or a passphrase, typed hidden.
	Secret Kind = iota
	// Text is a user name.
	Text
	// HostKey asks whether to trust a host ssh does not know; the answer
	// is "yes" or "no".
	HostKey
	// Confirm is a yes or no ssh asks for, as to use a key held by an
	// agent; the answer is the helper's exit code.
	Confirm
	// Notice tells the user to do something, as touch a hardware key,
	// with nothing to answer: ssh ends the helper once it is done.
	Notice
)

// Question is what a helper asks.
type Question struct {
	// Prompt is git's or ssh's text, as it gave it; show it through
	// redact.Shown.
	Prompt string `json:"prompt"`
	Kind   Kind   `json:"kind"`
	// Dir is the folder git ran in: the work tree of the repository that
	// asks, if any.
	Dir string `json:"dir"`
	// Token proves the helper was started by the app.
	Token string `json:"token,omitempty"`
}

// Host is the host the prompt names, the one the answer goes to; "" when
// it names none.
func (q Question) Host() string {
	p := q.Prompt
	if i, j := strings.IndexByte(p, '\''), strings.LastIndexByte(p, '\''); i >= 0 && j > i {
		quoted := p[i+1 : j]
		// git: "Password for 'https://user@host': ".
		if h, ok := redact.Host(quoted); ok && strings.Contains(quoted, "://") {
			return h
		}
		// ssh's host-key question: "host 'example.com (1.2.3.4)'".
		if strings.Contains(p, "authenticity of host") {
			h, _, _ := strings.Cut(quoted, " ")
			return h
		}
	}
	// ssh: "user@host's password: ".
	if at, end := strings.IndexByte(p, '@'), strings.Index(p, "'s password"); at >= 0 && end > at {
		return p[at+1 : end]
	}
	return ""
}

// kindOf reads what ssh or git asks for from the prompt and from
// SSH_ASKPASS_PROMPT, which ssh sets for a confirmation or a notice.
func kindOf(prompt, sshMode string) Kind {
	switch {
	case sshMode == "confirm":
		return Confirm
	case sshMode == "none":
		return Notice
	case strings.Contains(prompt, "(yes/no"):
		return HostKey
	case strings.HasPrefix(prompt, "Username"):
		return Text
	}
	return Secret
}

// answer is the app's reply; OK false is a question cancelled.
type answer struct {
	OK     bool   `json:"ok"`
	Answer string `json:"answer,omitempty"`
}

// Ask is a question waiting for the user. The first Answer or Cancel
// counts; one whose helper is gone is cancelled.
type Ask struct {
	Question
	conn   net.Conn
	once   sync.Once
	closed chan struct{}
	srv    *Server
}

// Gone is closed once the helper is gone: git or ssh no longer waits,
// as for a notice once the key was touched, or a command cancelled.
func (a *Ask) Gone() <-chan struct{} { return a.closed }

// Answer sends the user's answer.
func (a *Ask) Answer(text string) { a.reply(answer{OK: true, Answer: text}) }

// Cancel tells git or ssh the user declined.
func (a *Ask) Cancel() { a.reply(answer{}) }

func (a *Ask) reply(r answer) {
	a.once.Do(func() {
		a.conn.SetWriteDeadline(time.Now().Add(readLimit))
		json.NewEncoder(a.conn).Encode(r) // a helper gone hears nothing
		a.conn.Close()
		a.srv.done()
	})
}

// Server listens for helpers.
type Server struct {
	ln    net.Listener
	dir   string
	token string
	ask   func(*Ask)

	mu   sync.Mutex
	open int
}

// Listen starts listening in a new folder only the user can open; ask
// is called, on a goroutine of the server's, with each question.
func Listen(ask func(*Ask)) (*Server, error) {
	// A short base: a socket's path is limited to about a hundred bytes.
	dir, err := os.MkdirTemp("", "ramus-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	b := make([]byte, 32)
	rand.Read(b)
	s := &Server{ln: ln, dir: dir, token: hex.EncodeToString(b), ask: ask}
	go s.accept()
	return s, nil
}

// Env is what a git that may ask runs with: program is the app's own,
// an absolute path.
func (s *Server) Env(program string) []string {
	return []string{
		"SSH_ASKPASS=" + program,
		// ssh asks through SSH_ASKPASS without a terminal even when no
		// display is set: it has no terminal, since git runs in a session
		// of its own.
		"SSH_ASKPASS_REQUIRE=force",
		envSocket + "=" + s.ln.Addr().String(),
		envToken + "=" + s.token,
	}
}

// Asking reports whether a question is open: the user is answering, and
// the command that asked waits for them.
func (s *Server) Asking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open > 0
}

// Close stops listening; helpers still connected are cancelled.
func (s *Server) Close() error {
	err := s.ln.Close()
	os.RemoveAll(s.dir)
	return err
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(readLimit))
	r := bufio.NewReader(&limitedConn{conn, maxMessage})
	line, err := r.ReadBytes('\n')
	var q Question
	if err != nil || json.Unmarshal(line, &q) != nil ||
		subtle.ConstantTimeCompare([]byte(q.Token), []byte(s.token)) != 1 {
		conn.Close()
		return
	}
	q.Token = ""
	s.mu.Lock()
	full := s.open >= maxOpen
	if !full {
		s.open++
	}
	s.mu.Unlock()
	a := &Ask{Question: q, conn: conn, closed: make(chan struct{}), srv: s}
	if full {
		json.NewEncoder(conn).Encode(answer{})
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	go func() {
		// The helper sends nothing more: a read ends when it is gone.
		var b [1]byte
		conn.Read(b[:])
		close(a.closed)
		a.Cancel() // no-op once answered
	}()
	s.ask(a)
}

func (s *Server) done() {
	s.mu.Lock()
	s.open--
	s.mu.Unlock()
}

// limitedConn reads at most n bytes.
type limitedConn struct {
	c net.Conn
	n int
}

func (l *limitedConn) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errors.New("askpass: question too long")
	}
	p = p[:min(len(p), l.n)]
	n, err := l.c.Read(p)
	l.n -= n
	return n, err
}
