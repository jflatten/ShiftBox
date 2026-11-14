package backup

import "golang.org/x/sys/unix"

// The Linux CI exercises the same exclusive rename invariant.
func publishRestoreDirectory(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}
