// Package instancelock keeps one service instance per configuration directory,
// independently of the database backend. It does not open a database or change
// application configuration.
package instancelock

import (
	"errors"
	"os"
	"sync"
)

// Filename is deliberately stable across database backends and service exits.
// Never unlink this file to release a lock: a new inode would let two instances
// hold different locks for the same configuration directory.
const Filename = ".v2raya-instance.lock"

var (
	ErrAlreadyRunning = errors.New("another v2raya instance is already running for this configuration directory")
	ErrUnsupported    = errors.New("instance locking is not supported on this operating system")
)

// Lock owns an OS file lock until Close or process exit. A Lock must not be
// copied after first use. Its zero value and a nil *Lock may safely be closed.
type Lock struct {
	file     *os.File
	once     sync.Once
	closeErr error
}

// Acquire immediately attempts an exclusive lock in an existing configuration
// directory. Directory symlinks are supported; a symlink or other non-regular
// file at Filename is rejected. The lock file is never truncated or removed.
func Acquire(configDir string) (*Lock, error) {
	file, err := acquire(configDir)
	if err != nil {
		return nil, err
	}
	return &Lock{file: file}, nil
}

// Close releases the lock by closing its descriptor. It is safe to call
// concurrently and repeatedly; the stable lock file remains on disk.
func (lock *Lock) Close() error {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() {
		if lock.file != nil {
			lock.closeErr = lock.file.Close()
		}
	})
	return lock.closeErr
}
