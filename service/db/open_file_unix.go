//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package db

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openRegularDatabaseFile(path string, flag int, mode os.FileMode, noFollow bool) (*os.File, error) {
	// A FIFO/device substitution must not block before bbolt's lock timeout.
	flag |= unix.O_NONBLOCK | unix.O_CLOEXEC
	if noFollow {
		flag |= unix.O_NOFOLLOW
	}
	fd, err := unix.Open(path, flag, uint32(mode.Perm()))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err == nil {
			err = fmt.Errorf("refusing to open non-regular database %q", path)
		}
		return nil, err
	}
	return file, nil
}
