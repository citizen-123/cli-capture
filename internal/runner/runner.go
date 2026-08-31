// Package runner launches the target CLI inside a pseudo-terminal so it behaves
// exactly as it would in a normal terminal (colors, prompts, TUIs), while its
// network environment is rewritten to route through our proxy and trust our CA.
package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/creack/pty"

	"github.com/citizen-123/cli-capture/internal/shellquote"
)

// ErrTargetNotFound reports that a target name did not resolve to an
// executable on $PATH. Callers match it with errors.Is to offer the remedy
// their interface actually provides.
var ErrTargetNotFound = errors.New("not found in $PATH")

// Target is a running child process attached to a PTY.
type Target struct {
	Cmd  *exec.Cmd
	Pty  *os.File // read child output here; write keystrokes here
	done chan error
}

// ProxyEnv augments base (typically os.Environ()) with the variables that make
// well-behaved HTTP(S) clients route through proxyAddr and trust the MITM CA at
// caFile. The long tail of CA vars covers the common runtimes (curl, Python
// requests, Node, git) whose trust stores are configured differently.
func ProxyEnv(base []string, proxyAddr, caFile string) []string {
	proxyURL := "http://" + proxyAddr
	inject := map[string]string{
		"HTTP_PROXY":          proxyURL,
		"HTTPS_PROXY":         proxyURL,
		"http_proxy":          proxyURL,
		"https_proxy":         proxyURL,
		"ALL_PROXY":           proxyURL,
		"all_proxy":           proxyURL,
		"NO_PROXY":            "",     // clear so nothing opts out silently
		"SSL_CERT_FILE":       caFile, // OpenSSL / curl
		"CURL_CA_BUNDLE":      caFile,
		"REQUESTS_CA_BUNDLE":  caFile, // Python requests
		"NODE_EXTRA_CA_CERTS": caFile, // Node.js
		"GIT_SSL_CAINFO":      caFile, // git
		"CLI_CAPTURE_ACTIVE":  "1",    // marker so a target can detect capture
	}
	out := make([]string, 0, len(base)+len(inject))
	// Keep every base var except the ones we are about to set ourselves.
	for _, kv := range base {
		if _, override := inject[envKey(kv)]; !override {
			out = append(out, kv)
		}
	}
	for k, v := range inject {
		out = append(out, k+"="+v)
	}
	return out
}

// LoginShell returns the argv for the user's interactive shell, used as the
// default target when no command is given. Falls back to /bin/sh when $SHELL is
// unset. No -l: a nested login shell re-runs .zprofile/.zlogin, which the parent
// already ran; a PTY-attached shell is interactive on its own, so .zshrc (and
// its functions) load without any flag.
func LoginShell() []string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	return []string{sh}
}

// ShellCommand wraps argv into an interactive-shell invocation so that shell
// functions, aliases, and rc-file PATH entries resolve — things exec.LookPath
// cannot see. Returns e.g. {"/bin/zsh", "-i", "-c", "claude-as 'alt'"}.
//
// Escape hatch: this deliberately builds a shell command string, waiving the
// "pass args separately, never through a shell" default. The waiver *is* the
// feature — resolving a name only the user's shell knows requires the user's
// shell. The string is assembled from the user's own argv typed at their own
// terminal, so no privilege or trust boundary is crossed, and it is opt-in
// (-shell): the default path stays exec-direct with zero shell parsing.
func ShellCommand(argv []string) []string {
	if len(argv) == 0 {
		return LoginShell()
	}
	// argv[0] stays bare when it is a plain word. This is load-bearing: in zsh,
	// quoting any part of a word suppresses alias expansion, so a quoted
	// 'claude-as' would still find a function but silently miss a user's alias.
	// The charset is narrow enough that a bare argv[0] cannot carry a
	// metacharacter.
	parts := make([]string, 0, len(argv))
	if plainWord.MatchString(argv[0]) {
		parts = append(parts, argv[0])
	} else {
		parts = append(parts, shellquote.Single(argv[0]))
	}
	// Arguments are always quoted, so they stay literal words — no glob, no
	// word-splitting, no command substitution.
	for _, a := range argv[1:] {
		parts = append(parts, shellquote.Single(a))
	}
	return append(LoginShell(), "-i", "-c", strings.Join(parts, " "))
}

// plainWord matches a command name safe to leave unquoted: no shell
// metacharacters, no whitespace.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_.:@%+=/-]+$`)

// Start launches argv[0] with argv[1:] under a PTY, using env for its
// environment. Read/write child I/O via the returned Target.Pty.
func Start(argv []string, env []string) (*Target, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("runner: empty command")
	}
	// Resolve up front so the caller can tell an unresolvable name apart from a
	// PTY failure and suggest the remedy — exec's own "executable file not
	// found" says nothing about aliases and functions, which is what the name
	// usually turns out to be. The remedy itself is CLI wording, so it stays in
	// the command layer.
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, fmt.Errorf("%q: %w", argv[0], ErrTargetNotFound)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	t := &Target{Cmd: cmd, Pty: ptmx, done: make(chan error, 1)}
	go func() { t.done <- cmd.Wait() }()
	return t, nil
}

// Resize propagates a terminal size change to the child.
func (t *Target) Resize(rows, cols uint16) error {
	return pty.Setsize(t.Pty, &pty.Winsize{Rows: rows, Cols: cols})
}

// Wait blocks until the child exits.
func (t *Target) Wait() error { return <-t.done }

// Close terminates the child and releases the PTY.
func (t *Target) Close() error {
	if t.Cmd.Process != nil {
		_ = t.Cmd.Process.Kill()
	}
	return t.Pty.Close()
}

func envKey(kv string) string {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i]
		}
	}
	return kv
}
