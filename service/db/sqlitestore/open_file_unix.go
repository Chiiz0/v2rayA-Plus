//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package sqlitestore

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func openRegularReadOnly(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("SQLite runtime requires regular files without symlinks")
	}
	return file, nil
}
