package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
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
			"-theme", "not-a-real-theme",
			"--", "true",
		}
		main()
		return
	}

	dir := filepath.Join(t.TempDir(), "new", "private")
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

	logPath := filepath.Join(dir, "cli-capture.log")
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read startup log: %v", err)
	}
	if got := string(contents); !strings.Contains(got, "config:") {
		t.Fatalf("startup log does not contain config diagnostic: %q", got)
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
