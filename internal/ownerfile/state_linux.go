//go:build linux

package ownerfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// StateDir is a trusted state-directory descriptor. Its methods access only
// direct regular-file children through the open directory descriptor, never by
// resolving an attacker-controlled path.
type StateDir struct {
	path  string
	fd    int
	owner uint32
}

// OpenRootStateDir opens path as a root-owned, non-symlink, private state
// directory. Every existing ancestor must be root-owned and not writable by a
// non-root user; missing components are created with mode 0700.
func OpenRootStateDir(path string) (*StateDir, error) {
	return openTrustedStateDir(path, 0)
}

func openTrustedStateDir(path string, owner uint32) (_ *StateDir, err error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean == "/" {
		return nil, fmt.Errorf("state directory must be a non-root absolute path: %q", path)
	}

	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open state directory root: %w", err)
	}
	defer func() {
		if err != nil && fd >= 0 {
			_ = unix.Close(fd)
		}
	}()

	components := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if err := validateStateDirectory(fd, owner, false); err != nil {
		return nil, fmt.Errorf("untrusted state directory /: %w", err)
	}
	for i, component := range components {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr == unix.ENOENT {
			if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil && mkdirErr != unix.EEXIST {
				return nil, fmt.Errorf("create state directory %q: %w", component, mkdirErr)
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			return nil, fmt.Errorf("open state directory component %q: %w", component, openErr)
		}
		if closeErr := unix.Close(fd); closeErr != nil {
			_ = unix.Close(next)
			fd = -1
			return nil, fmt.Errorf("close state directory parent: %w", closeErr)
		}
		fd = next

		final := i == len(components)-1
		if err := validateStateDirectory(fd, owner, final); err != nil {
			return nil, fmt.Errorf("untrusted state directory %q: %w", filepath.Join("/", strings.Join(components[:i+1], "/")), err)
		}
	}

	return &StateDir{path: clean, fd: fd, owner: owner}, nil
}

func validateStateDirectory(fd int, owner uint32, final bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("not a directory")
	}
	if stat.Uid != owner {
		return fmt.Errorf("owner uid %d, want %d", stat.Uid, owner)
	}
	if stat.Mode&0o022 != 0 {
		return fmt.Errorf("mode %04o permits non-owner writes", stat.Mode&0o777)
	}
	if final && stat.Mode&0o777 != 0o700 {
		if err := unix.Fchmod(fd, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Path returns the verified absolute directory path. It is suitable for APIs
// that must pass the CA pathname to a child process.
func (d *StateDir) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// Close releases the directory descriptor.
func (d *StateDir) Close() error {
	if d == nil || d.fd < 0 {
		return nil
	}
	err := unix.Close(d.fd)
	d.fd = -1
	return err
}

// OpenFile opens a direct regular-file child without following symlinks. An
// existing child must have the state directory's trusted owner.
func (d *StateDir) OpenFile(name string, flags int, mode os.FileMode) (*os.File, error) {
	if d == nil || d.fd < 0 {
		return nil, fmt.Errorf("state directory is closed")
	}
	if filepath.Base(name) != name || name == "." || name == "" {
		return nil, fmt.Errorf("state filename must be a direct child: %q", name)
	}
	fd, err := unix.Openat(d.fd, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("state file %q is not regular", name)
	}
	if stat.Uid != d.owner {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("state file %q owner uid %d, want %d", name, stat.Uid, d.owner)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.path, name))
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap state file %q", name)
	}
	return f, nil
}

// ReadFile reads a regular state file without following a symlink.
func (d *StateDir) ReadFile(name string) ([]byte, error) {
	f, err := d.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// AppendFile opens a private state file for appending, creating it without
// following a symlink and repairing its mode after owner validation.
func (d *StateDir) AppendFile(name string, mode os.FileMode) (*os.File, error) {
	f, err := d.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, mode)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// WriteFile replaces a direct state file's contents after no-follow opening and
// owner validation. It is intended for small, root-owned state files such as a
// CA certificate and key.
func (d *StateDir) WriteFile(name string, data []byte, mode os.FileMode) (err error) {
	f, err := d.OpenFile(name, os.O_WRONLY|os.O_CREATE, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if n, err := f.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	return f.Sync()
}
