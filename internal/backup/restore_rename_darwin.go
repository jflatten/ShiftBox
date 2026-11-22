package backup

import "golang.org/x/sys/unix"

func publishRestoreDirectory(source, destination string) error {
	return unix.RenamexNp(source, destination, unix.RENAME_EXCL)
}
