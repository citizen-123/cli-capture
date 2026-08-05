// Package ownerfile atomically writes capture data to owner-only files.
//
// Owner-only permissions are a POSIX guarantee. Windows does not implement
// POSIX mode bits; callers that need an equivalent guarantee there must use
// Windows ACLs.
package ownerfile

import (
	"io"
	"os"
	"path/filepath"
)

// Mode is owner read/write with no access for group or other users on POSIX.
const Mode os.FileMode = 0o600

// WriteFunc writes a complete file to a private temporary inode and atomically
// renames it over path after write returns successfully. A callback error
// leaves the destination unchanged.
func WriteFunc(path string, write func(io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(Mode); err != nil {
		return err
	}
	if err := write(tmp); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// Write atomically writes data to path with owner-only permissions on POSIX.
func Write(path string, data []byte) error {
	return WriteFunc(path, func(w io.Writer) error {
		n, err := w.Write(data)
		if err == nil && n != len(data) {
			return io.ErrShortWrite
		}
		return err
	})
}
