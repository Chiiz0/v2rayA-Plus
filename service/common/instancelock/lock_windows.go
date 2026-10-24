package instancelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func acquire(configDir string) (*os.File, error) {
	// Stat follows directory symlinks, matching Unix configuration aliases.
	info, err := os.Stat(configDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("instance lock directory is not a directory: %s", configDir)
	}
	path := filepath.Join(configDir, Filename)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// A nil SecurityAttributes makes the handle non-inheritable. Do not share
	// DELETE: deleting a held lock file could otherwise create a second inode.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open instance lock", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	var metadata windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &metadata); err != nil {
		_ = file.Close()
		return nil, err
	}
	kind, err := windows.GetFileType(handle)
	if err != nil || kind != windows.FILE_TYPE_DISK || metadata.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("instance lock must be a regular file, not a reparse point: %s", path)
	}
	// Lock a fixed byte range; Windows permits locks beyond EOF, so the file
	// remains empty and no PID or other mutable state needs to be written.
	var overlap windows.Overlapped
	err = windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, configDir)
		}
		return nil, &os.PathError{Op: "acquire instance lock", Path: path, Err: err}
	}
	return file, nil
}
