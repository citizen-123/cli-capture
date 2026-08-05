//go:build !windows

package ownerfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesDestinationInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.txt")
	const old = "legacy contents"
	const replacement = "new captured secret"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	oldInfo, err := legacy.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if err := Write(path, []byte(replacement)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	newInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(oldInfo, newInfo) {
		t.Error("destination inode was reused")
	}
	if _, err := legacy.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(replacement)+len(old))
	n, err := legacy.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != old {
		t.Errorf("pre-opened descriptor reads %q, want old contents %q", got, old)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != replacement {
		t.Errorf("destination reads %q, want %q", got, replacement)
	}
}

func TestWriteFuncLeavesDestinationOnWriteError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, []byte("valid old session"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("encode failed")
	err := WriteFunc(path, func(w io.Writer) error {
		if _, err := io.WriteString(w, "partial secret"); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WriteFunc error = %v, want %v", err, wantErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "valid old session" {
		t.Errorf("destination changed to %q after callback error", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Errorf("directory entries after callback error = %v, want only %q", entries, filepath.Base(path))
	}
}

func TestSyncParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.txt")
	if err := os.WriteFile(path, []byte("capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncParent(path); err != nil {
		t.Fatalf("syncParent: %v", err)
	}
}

func TestWriteReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	path := filepath.Join(dir, "capture.txt")
	if err := os.WriteFile(target, []byte("target contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if err := Write(path, []byte("captured secret")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	targetData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(targetData); got != "target contents" {
		t.Errorf("symlink target was overwritten with %q", got)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("destination remains a symlink")
	}
}
