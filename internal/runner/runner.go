// Package runner launches the target CLI inside a pseudo-terminal so it behaves
// exactly as it would in a normal terminal (colors, prompts, TUIs), while its
// network environment is rewritten to route through our proxy and trust our CA.
package runner

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

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

// Start launches argv[0] with argv[1:] under a PTY, using env for its
// environment. Read/write child I/O via the returned Target.Pty.
func Start(argv []string, env []string) (*Target, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("runner: empty command")
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
