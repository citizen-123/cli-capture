//go:build !linux

package ownerfile

import (
	"fmt"
	"os"
)

// StateDir is unavailable outside Linux because transparent rule application is
// Linux-only.
type StateDir struct{}

func OpenRootStateDir(string) (*StateDir, error) {
	return nil, fmt.Errorf("trusted privileged state directories are supported only on Linux")
}

func (d *StateDir) Path() string { return "" }
func (d *StateDir) Close() error { return nil }

func (d *StateDir) OpenFile(string, int, os.FileMode) (*os.File, error) {
	return nil, fmt.Errorf("trusted privileged state directories are supported only on Linux")
}

func (d *StateDir) ReadFile(string) ([]byte, error) {
	return nil, fmt.Errorf("trusted privileged state directories are supported only on Linux")
}

func (d *StateDir) AppendFile(string, os.FileMode) (*os.File, error) {
	return nil, fmt.Errorf("trusted privileged state directories are supported only on Linux")
}

func (d *StateDir) WriteFile(string, []byte, os.FileMode) error {
	return fmt.Errorf("trusted privileged state directories are supported only on Linux")
}
