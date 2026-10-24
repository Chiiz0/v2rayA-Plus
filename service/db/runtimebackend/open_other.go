//go:build !linux

package runtimebackend

import (
	"errors"
	"os"
)

func openRegular(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("database input must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		if err == nil {
			err = errors.New("database input changed while opening")
		}
		return nil, err
	}
	return file, nil
}
