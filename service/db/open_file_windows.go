package db

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func openRegularDatabaseFile(path string, flag int, mode os.FileMode, noFollow bool) (*os.File, error) {
	if !noFollow {
		file, err := os.OpenFile(path, flag, mode)
		if err != nil {
			return nil, err
		}
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
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// OPEN_EXISTING cannot create a missing source. OPEN_REPARSE_POINT opens
	// the link itself so it can be rejected without following its target.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
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
		return nil, fmt.Errorf("refusing to read non-regular database or reparse point %q", path)
	}
	return file, nil
}
