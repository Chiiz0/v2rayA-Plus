//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package instancelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func acquire(configDir string) (*os.File, error) {
	// Follow directory aliases, then use the opened directory for the lock file
	// lookup. O_DIRECTORY also rejects missing paths and non-directories.
	directory, err := unix.Open(configDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open instance lock directory", Path: configDir, Err: err}
	}
	defer unix.Close(directory)
	path := filepath.Join(configDir, Filename)
	// O_NONBLOCK prevents a substituted FIFO from hanging before the regular
	// file check. O_NOFOLLOW rejects the final symlink at the actual open.
	fd, err := unix.Openat(directory, Filename, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, &os.PathError{Op: "open instance lock", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("instance lock must be a regular file: %s", path)
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, configDir)
		}
		return nil, &os.PathError{Op: "acquire instance lock", Path: path, Err: err}
	}
	return file, nil
}
