//go:build linux

package ownerfile

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRootStateDirRejectsSymlinkAndNarrowsMode(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned state directory validation requires root")
	}
	rootAccount, err := user.LookupId("0")
	if err != nil {
		t.Fatalf("look up root account: %v", err)
	}
	root, err := os.MkdirTemp(rootAccount.HomeDir, ".trusted-state-test-")
	if err != nil {
		t.Fatalf("create root-owned test directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	statePath := filepath.Join(root, "state")
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	state, err := OpenRootStateDir(statePath)
	if err != nil {
		t.Fatalf("open trusted state directory: %v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close state directory: %v", err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat state directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("state directory mode = %04o, want 0700", got)
	}

	linkPath := filepath.Join(root, "state-link")
	if err := os.Symlink(statePath, linkPath); err != nil {
		t.Fatalf("create state symlink: %v", err)
	}
	if _, err := OpenRootStateDir(linkPath); err == nil {
		t.Fatal("trusted state directory accepted a symlink")
	}
}

func TestStateDirRejectsSymlinkedLog(t *testing.T) {
	root := t.TempDir()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open test state directory: %v", err)
	}
	state := &StateDir{path: root, fd: fd, owner: uint32(os.Geteuid())}
	defer state.Close()

	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(state.Path(), "cli-capture.log")); err != nil {
		t.Fatalf("create log symlink: %v", err)
	}
	if _, err := state.AppendFile("cli-capture.log", 0o600); err == nil {
		t.Fatal("state directory followed a symlinked log")
	}
	contents, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if got := string(contents); got != "outside" {
		t.Errorf("symlink target changed to %q", got)
	}
}

func TestStateDirRejectsSymlinkedCAState(t *testing.T) {
	root := t.TempDir()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open test state directory: %v", err)
	}
	state := &StateDir{path: root, fd: fd, owner: uint32(os.Geteuid())}
	defer state.Close()

	outside := filepath.Join(root, "outside-key")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(state.Path(), "ca.key")); err != nil {
		t.Fatalf("create CA-key symlink: %v", err)
	}
	if err := state.WriteFile("ca.key", []byte("replacement"), 0o600); err == nil {
		t.Fatal("state directory followed a symlinked CA key")
	}
	contents, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if got := string(contents); got != "outside" {
		t.Errorf("symlink target changed to %q", got)
	}
}
