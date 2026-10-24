package backup

import (
	"fmt"
	"os"
	"syscall"
)

// O_NOFOLLOW rejects a substituted symlink at open time. O_NONBLOCK prevents
// a substituted pipe or device from blocking before its type can be checked.
func openRegularReadOnly(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("refusing to read non-regular file %q", path)
	}
	return file, nil
}
