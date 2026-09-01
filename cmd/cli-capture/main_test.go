package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/runner"
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

func TestTransparentTargetCredentialsLeaveNonApplyPathsUnchanged(t *testing.T) {
	tests := []struct {
		name  string
		addr  string
		apply bool
		uid   int
	}{
		{name: "ordinary proxy mode", apply: false, uid: -1},
		{name: "apply without transparent listener remains ignored", apply: true, uid: -1},
		{name: "manual transparent rules", addr: "127.0.0.1:8081", apply: false, uid: 1500},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := transparentTargetCredentials(tc.addr, tc.apply, tc.uid)
			if err != nil {
				t.Fatalf("transparentTargetCredentials: %v", err)
			}
			if got != nil {
				t.Fatal("non-apply path selected child credentials")
			}
			ordinaryCalled := false
			credentialedCalled := false
			_, err = startTarget(
				[]string{"target"},
				nil,
				got,
				func([]string, []string) (*runner.Target, error) {
					ordinaryCalled = true
					return nil, nil
				},
				func([]string, []string, *runner.UserCredentials) (*runner.Target, error) {
					credentialedCalled = true
					return nil, nil
				},
			)
			if err != nil {
				t.Fatalf("startTarget: %v", err)
			}
			if !ordinaryCalled || credentialedCalled {
				t.Fatalf("runner paths: ordinary=%t credentialed=%t, want true/false", ordinaryCalled, credentialedCalled)
			}
		})
	}
}

func TestTransparentTargetCredentialsRequiresUIDForApply(t *testing.T) {
	credentials, err := transparentTargetCredentials("127.0.0.1:8081", true, -1)
	if err == nil || !strings.Contains(err.Error(), "-transparent-apply requires -transparent-uid") {
		t.Fatalf("missing uid error = %v", err)
	}
	if credentials != nil {
		t.Fatal("invalid apply configuration returned credentials")
	}
}

func TestTransparentTargetCredentialsRejectsProxyUID(t *testing.T) {
	credentials, err := transparentTargetCredentials("127.0.0.1:8081", true, 0)
	if err == nil || !strings.Contains(err.Error(), "must name a non-root user") {
		t.Fatalf("root uid error = %v", err)
	}
	if credentials != nil {
		t.Fatal("root uid returned credentials")
	}
}

func TestExecuteCaptureTargetNotFoundTearsDownRedirect(t *testing.T) {
	var events []string
	lifecycle := recordingLifecycle(&events, runner.ErrTargetNotFound, nil)

	err := executeCapture(lifecycle)
	if !errors.Is(err, runner.ErrTargetNotFound) {
		t.Fatalf("executeCapture error = %v, want target-not-found sentinel", err)
	}
	assertLifecycleEvents(t, events, []string{
		"proxy start",
		"transparent start",
		"target start",
		"signals stop",
		"rules teardown",
		"listener close",
		"proxy close",
	})
}

func TestExecuteCaptureProgramOutcomesCleanExactlyOnceInOrder(t *testing.T) {
	programErr := errors.New("program failed")
	tests := []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "program error", err: programErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			err := executeCapture(recordingLifecycle(&events, nil, tc.err))
			if !errors.Is(err, tc.err) {
				t.Fatalf("executeCapture error = %v, want %v", err, tc.err)
			}
			assertLifecycleEvents(t, events, []string{
				"proxy start",
				"transparent start",
				"target start",
				"terminal start",
				"program run",
				"terminal close",
				"target and PTY close",
				"signals stop",
				"rules teardown",
				"listener close",
				"proxy close",
			})
		})
	}
}

func TestExecuteCaptureSignalQuitsThenCleansExactlyOnce(t *testing.T) {
	var events []string
	lifecycle := recordingLifecycle(&events, nil, nil)
	signals := make(chan os.Signal, 1)
	lifecycle.startTransparent = func() (transparentLifecycle, error) {
		events = append(events, "transparent start")
		return transparentLifecycle{
			closeListener: func() { events = append(events, "listener close") },
			teardown:      func() { events = append(events, "rules teardown") },
			signals:       signals,
			stopSignals:   func() { events = append(events, "signals stop") },
		}, nil
	}
	lifecycle.runProgram = func(sigCh <-chan os.Signal) error {
		events = append(events, "program run")
		quit := make(chan struct{})
		return runProgram(
			func() error {
				signals <- syscall.SIGTERM
				<-quit
				return nil
			},
			func() { close(quit) },
			sigCh,
		)
	}

	err := executeCapture(lifecycle)
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("executeCapture error = %v, want interrupted sentinel", err)
	}
	assertLifecycleEvents(t, events, []string{
		"proxy start",
		"transparent start",
		"target start",
		"terminal start",
		"program run",
		"terminal close",
		"target and PTY close",
		"signals stop",
		"rules teardown",
		"listener close",
		"proxy close",
	})
}

func TestExecuteCaptureWaitsForSignalQuitBeforeCleanup(t *testing.T) {
	var events []string
	lifecycle := recordingLifecycle(&events, nil, nil)
	signals := make(chan os.Signal, 1)
	lifecycle.startTransparent = func() (transparentLifecycle, error) {
		return transparentLifecycle{signals: signals}, nil
	}

	quitEntered := make(chan struct{})
	releaseQuit := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseQuit)
		}
	}()
	runReturned := make(chan struct{})
	cleanupStarted := make(chan struct{})
	lifecycle.closeTerminal = func() { close(cleanupStarted) }
	lifecycle.runProgram = func(sigCh <-chan os.Signal) error {
		return runProgram(
			func() error {
				signals <- syscall.SIGTERM
				<-quitEntered
				close(runReturned)
				return nil
			},
			func() {
				close(quitEntered)
				<-releaseQuit
			},
			sigCh,
		)
	}

	result := make(chan error, 1)
	go func() { result <- executeCapture(lifecycle) }()
	<-runReturned

	select {
	case <-cleanupStarted:
		t.Fatal("resource cleanup started while the signal handler was still in quit")
	case err := <-result:
		t.Fatalf("executeCapture returned %v while the signal handler was still in quit", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseQuit)
	released = true
	select {
	case err := <-result:
		if !errors.Is(err, errInterrupted) {
			t.Fatalf("executeCapture error = %v, want interrupted sentinel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("executeCapture did not return after quit completed")
	}
	select {
	case <-cleanupStarted:
	default:
		t.Fatal("resource cleanup did not run after quit completed")
	}
}

func recordingLifecycle(events *[]string, targetErr, programErr error) captureLifecycle {
	return captureLifecycle{
		startProxy: func() error {
			*events = append(*events, "proxy start")
			return nil
		},
		closeProxy: func() { *events = append(*events, "proxy close") },
		startTransparent: func() (transparentLifecycle, error) {
			*events = append(*events, "transparent start")
			return transparentLifecycle{
				closeListener: func() { *events = append(*events, "listener close") },
				teardown:      func() { *events = append(*events, "rules teardown") },
				signals:       make(chan os.Signal),
				stopSignals:   func() { *events = append(*events, "signals stop") },
			}, nil
		},
		startTarget: func() error {
			*events = append(*events, "target start")
			return targetErr
		},
		closeTarget: func() { *events = append(*events, "target and PTY close") },
		startTerminal: func() {
			*events = append(*events, "terminal start")
		},
		closeTerminal: func() { *events = append(*events, "terminal close") },
		runProgram: func(<-chan os.Signal) error {
			*events = append(*events, "program run")
			return runProgram(func() error { return programErr }, func() {}, nil)
		},
	}
}

func assertLifecycleEvents(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lifecycle events:\n%q\nwant:\n%q", got, want)
	}
}
