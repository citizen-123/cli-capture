// Package ownerfile writes capture data to owner-only files.
package ownerfile

import (
	"io"
	"os"
)

// Mode is owner read/write with no access for group or other users.
const Mode os.FileMode = 0o600

// Create opens path for a private rewrite. It opens without O_TRUNC so a
// pre-existing permissive file is narrowed through the opened descriptor
// before any new captured data is exposed or its old contents are destroyed.
// The caller owns the returned file.
func Create(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, Mode)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		_ = f.Close()
		return nil, err
	}
	if err := f.Chmod(Mode); err != nil {
		return fail(err)
	}
	if err := f.Truncate(0); err != nil {
		return fail(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return f, nil
}

// Write is os.WriteFile with an enforced owner-only mode for both new and
// pre-existing files.
func Write(path string, data []byte) error {
	f, err := Create(path)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
