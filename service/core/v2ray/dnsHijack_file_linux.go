package v2ray

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func writeResolvFileNoFollow(path string, content []byte, mode os.FileMode) error {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to write non-regular resolv.conf")
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		return err
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		return err
	}
	return f.Sync()
}
