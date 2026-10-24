//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package instancelock

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLockDescriptorCloseOnExec(t *testing.T) {
	lock, err := Acquire(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	flags, err := unix.FcntlInt(lock.file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("lock descriptor is inheritable: flags=%d error=%v", flags, err)
	}
}

func TestLockRejectsFIFOWithoutBlocking(t *testing.T) {
	directory := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(directory, Filename), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if output, err := lockHelperCommand(ctx, directory, "invalid").CombinedOutput(); err != nil {
		t.Fatalf("FIFO rejection: %v\n%s", err, output)
	}
}
