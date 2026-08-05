package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestStartupRedirectsDiagnosticsBeforeConfiguration runs the real main in a
// child test process and deliberately fails theme resolution. That point is
// after the config diagnostic but before CA creation, so it proves all of the
// startup-order contract at once: the diagnostic already has a private file
// destination, while fatal validation errors remain loud on stderr.
func TestStartupRedirectsDiagnosticsBeforeConfiguration(t *testing.T) {
	const helperEnv = "CLI_CAPTURE_STARTUP_LOG_HELPER"
	if os.Getenv(helperEnv) == "1" {
		flag.CommandLine = flag.NewFlagSet("cli-capture", flag.ContinueOnError)
		os.Args = []string{
			"cli-capture",
			"-dir", os.Getenv("CLI_CAPTURE_STARTUP_LOG_DIR"),
			"--", "true",
		}
		main()
		return
	}

	dir := filepath.Join(t.TempDir(), "new", "private")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create existing data directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"theme":{"base":"not-a-real-theme"}}`), 0o600); err != nil {
		t.Fatalf("seed invalid legacy config: %v", err)
	}
	const priorLog = "previous diagnostics\n"
	logPath := filepath.Join(dir, "cli-capture.log")
	if err := os.WriteFile(logPath, []byte(priorLog), 0o644); err != nil {
		t.Fatalf("seed existing startup log: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupRedirectsDiagnosticsBeforeConfiguration$")
	cmd.Env = append(os.Environ(), helperEnv+"=1", "CLI_CAPTURE_STARTUP_LOG_DIR="+dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("startup with an invalid theme succeeded, want a loud validation failure")
	}

	if got := stderr.String(); strings.Contains(got, "config:") {
		t.Fatalf("config metadata leaked to stderr before log redirection:\n%s", got)
	} else if !strings.Contains(got, "theme: unknown theme") {
		t.Fatalf("fatal theme error was not reported on stderr:\n%s", got)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("startup wrote to stdout before the TUI: %q", got)
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read startup log: %v", err)
	}
	if got := string(contents); !strings.Contains(got, priorLog) {
		t.Fatalf("early validation failure discarded prior diagnostics: %q", got)
	} else if !strings.Contains(got, "config:") {
		t.Fatalf("startup log does not contain config diagnostic: %q", got)
	}

	if runtime.GOOS == "windows" {
		return // Windows os.Chmod controls read-only state, not Unix mode bits.
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat data directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("data directory mode = %04o, want 0700", got)
	}
	logInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat startup log: %v", err)
	}
	if got := logInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("startup log mode = %04o, want 0600", got)
	}
}
