// Package ownerfile writes capture data to owner-only files.
//
// Owner-only permissions and atomic replacement are POSIX guarantees. Windows
// does not implement POSIX mode bits, and os.Rename replacement semantics are
// platform-dependent; callers that need equivalent guarantees there must use
// Windows ACLs and a Windows-specific replacement operation.
package ownerfile

import (
	"io"
	"os"
	"path/filepath"
)

// Mode is owner read/write with no access for group or other users on POSIX.
const Mode os.FileMode = 0o600

// WriteFunc writes a complete file to a private temporary inode and, on POSIX,
// atomically renames it over path after write returns successfully. A callback
// error leaves the destination unchanged.
//
// On POSIX, a successful rename is followed by a parent-directory fsync for
// crash durability. If that sync fails, WriteFunc returns its error even though
// the replacement has already been committed and must not be assumed absent.
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
	return syncParent(path)
}

// Write writes data to path with WriteFunc's platform-specific guarantees.
func Write(path string, data []byte) error {
	return WriteFunc(path, func(w io.Writer) error {
		n, err := w.Write(data)
		if err == nil && n != len(data) {
			return io.ErrShortWrite
		}
		return err
	})
}
