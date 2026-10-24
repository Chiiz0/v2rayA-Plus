package sqlitestore

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

func openRegularReadOnly(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
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
		return nil, errors.New("SQLite runtime requires regular files without reparse points")
	}
	return file, nil
}
