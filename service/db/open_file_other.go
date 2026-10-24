//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package db

import (
	"fmt"
	"os"
)

func openRegularDatabaseFile(path string, flag int, mode os.FileMode, noFollow bool) (*os.File, error) {
	// Portable fallback: inspect before opening and verify that the opened
	// object is the same regular file. Supported Unix platforms also prevent symlink
	// following and blocking on substituted special files at the syscall.
	var before os.FileInfo
	var err error
	if noFollow {
		before, err = os.Lstat(path)
	} else {
		before, err = os.Stat(path)
	}
	if err != nil && !(os.IsNotExist(err) && flag&os.O_CREATE != 0) {
		return nil, err
	}
	if before != nil && !before.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to open non-regular database %q", path)
	}
	file, err := os.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || (before != nil && !os.SameFile(before, after)) {
		_ = file.Close()
		if err == nil {
			err = fmt.Errorf("database source changed or is not regular: %q", path)
		}
		return nil, err
	}
	return file, nil
}
