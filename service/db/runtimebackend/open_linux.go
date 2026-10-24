//go:build linux

package runtimebackend

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openRegular(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err == nil {
			err = errors.New("database input must be a regular file")
		}
		return nil, err
	}
	return file, nil
}
