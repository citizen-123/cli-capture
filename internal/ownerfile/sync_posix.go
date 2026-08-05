//go:build !windows

package ownerfile

import (
	"os"
	"path/filepath"
)

func syncParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
