//go:build windows

package ownerfile

// Windows does not provide POSIX directory-fsync durability through os.File.
func syncParent(string) error { return nil }
